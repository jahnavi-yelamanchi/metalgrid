// Command devplugin is a mock Kubernetes device plugin. It advertises N fake
// "metalgrid.dev/accelerator" devices per node so AcceleratorJob pods can be
// scheduled without real GPU/Tenstorret hardware.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	resourceName = "metalgrid.dev/accelerator"
	pluginSock   = "metalgrid-mock.sock"
	kubeletSock  = "kubelet.sock"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	numDevices := 4
	if v := os.Getenv("NUM_DEVICES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			logger.Error("invalid NUM_DEVICES", "value", v, "error", err)
			os.Exit(1)
		}
		numDevices = n
	}

	socketDir := "/var/lib/kubelet/device-plugins"
	if v := os.Getenv("DEVICE_PLUGIN_DIR"); v != "" {
		socketDir = v
	}

	p := &plugin{logger: logger, devices: mockDevices(numDevices)}
	if err := p.run(socketDir); err != nil {
		logger.Error("device plugin exited", "error", err)
		os.Exit(1)
	}
}

func mockDevices(n int) []*pluginapi.Device {
	devices := make([]*pluginapi.Device, n)
	for i := range devices {
		devices[i] = &pluginapi.Device{
			ID:     fmt.Sprintf("mock-%d", i),
			Health: pluginapi.Healthy,
		}
	}
	return devices
}

type plugin struct {
	pluginapi.UnimplementedDevicePluginServer
	logger  *slog.Logger
	devices []*pluginapi.Device
}

func (p *plugin) run(socketDir string) error {
	sockPath := filepath.Join(socketDir, pluginSock)
	_ = os.Remove(sockPath)

	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", sockPath, err)
	}

	srv := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(srv, p)

	go func() {
		p.logger.Info("serving device plugin grpc", "socket", sockPath)
		if err := srv.Serve(lis); err != nil {
			p.logger.Error("grpc server stopped", "error", err)
		}
	}()

	if err := p.register(socketDir, filepath.Base(sockPath)); err != nil {
		srv.Stop()
		return fmt.Errorf("registering with kubelet: %w", err)
	}

	select {}
}

func (p *plugin) register(socketDir, sockName string) error {
	conn, err := grpc.NewClient("unix://"+filepath.Join(socketDir, kubeletSock),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	_, err = client.Register(context.Background(), &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     sockName,
		ResourceName: resourceName,
	})
	return err
}

func (p *plugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

func (p *plugin) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: p.devices}); err != nil {
		return err
	}
	// Fake devices never change health; block until the client disconnects.
	<-stream.Context().Done()
	return stream.Context().Err()
}

func (p *plugin) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{}
	for _, r := range req.ContainerRequests {
		resp.ContainerResponses = append(resp.ContainerResponses, &pluginapi.ContainerAllocateResponse{
			Envs: map[string]string{
				"METALGRID_ACCELERATOR_IDS": fmt.Sprint(r.DevicesIds),
			},
		})
	}
	return resp, nil
}

func (p *plugin) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
