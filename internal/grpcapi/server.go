// Package grpcapi implements the MetalGrid gRPC API. Like internal/api, it's
// a thin transport wrapper: business logic lives in internal/service so REST
// and gRPC never diverge on job semantics.
//
// ponytail: no auth interceptor yet — REST's JWT + per-team rate limit
// (internal/api/auth.go, ratelimit.go) aren't mirrored here, so this service
// currently trusts its network (intended for in-cluster callers only).
// Upgrade path: extract authenticate() into a shared package and add a
// grpc.UnaryServerInterceptor that calls it against the "authorization"
// metadata key, same contract as the REST middleware.
package grpcapi

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jahnavi-yelamanchi/metalgrid/internal/grpcapi/pb"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/service"
)

type Server struct {
	pb.UnimplementedJobsServiceServer
	Jobs *service.JobService
}

func toProtoJob(j service.Job) *pb.Job {
	return &pb.Job{
		Id:               j.ID,
		Team:             j.Team,
		Image:            j.Image,
		AcceleratorType:  j.AcceleratorType,
		AcceleratorCount: j.AcceleratorCount,
		Priority:         j.Priority,
		Status:           j.Status,
		Message:          j.Message,
		CreatedAt:        j.CreatedAt.Format(time.RFC3339),
	}
}

func (s *Server) CreateJob(ctx context.Context, req *pb.CreateJobRequest) (*pb.Job, error) {
	job, err := s.Jobs.CreateJob(ctx, service.CreateJobInput{
		Team:             req.Team,
		Image:            req.Image,
		Command:          req.Command,
		Args:             req.Args,
		AcceleratorType:  req.AcceleratorType,
		AcceleratorCount: req.AcceleratorCount,
		Priority:         req.Priority,
		IdempotencyKey:   req.IdempotencyKey,
	})
	if errors.Is(err, service.ErrValidation) {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return toProtoJob(job), nil
}

func (s *Server) GetJob(ctx context.Context, req *pb.GetJobRequest) (*pb.Job, error) {
	job, err := s.Jobs.GetJob(ctx, req.Id)
	if errors.Is(err, service.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "job not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return toProtoJob(job), nil
}

func (s *Server) ListJobs(ctx context.Context, req *pb.ListJobsRequest) (*pb.ListJobsResponse, error) {
	jobs, next, err := s.Jobs.ListJobs(ctx, req.Team, req.Cursor, int(req.Limit))
	if errors.Is(err, service.ErrValidation) {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*pb.Job, len(jobs))
	for i, j := range jobs {
		out[i] = toProtoJob(j)
	}
	return &pb.ListJobsResponse{Jobs: out, NextCursor: next}, nil
}

func (s *Server) DeleteJob(ctx context.Context, req *pb.DeleteJobRequest) (*pb.DeleteJobResponse, error) {
	err := s.Jobs.DeleteJob(ctx, req.Id)
	if errors.Is(err, service.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "job not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.DeleteJobResponse{}, nil
}

func (s *Server) GetCapacity(ctx context.Context, _ *pb.GetCapacityRequest) (*pb.Capacity, error) {
	cap, err := s.Jobs.Capacity(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.Capacity{Total: cap.Total, Available: cap.Available}, nil
}
