terraform {
  required_version = ">= 1.5"
}

variable "cluster_name" {
  description = "kind cluster name"
  type        = string
  default     = "metalgrid"
}

# Wraps `kind create cluster` rather than re-declaring the cluster topology
# in HCL: hack/kind-config.yaml already encodes the scheduler-extender
# kubeadm patches (see internal/scheduler) and is the tested source of
# truth. Duplicating that as a second, drift-prone copy just to have "pure"
# Terraform would be worse IaC, not better.
resource "null_resource" "kind_cluster" {
  triggers = {
    cluster_name     = var.cluster_name
    kind_config_sha  = filesha256("${path.module}/../../hack/kind-config.yaml")
    kind_config_path = abspath("${path.module}/../../hack/kind-config.yaml")
  }

  provisioner "local-exec" {
    command = "kind get clusters | grep -qx '${self.triggers.cluster_name}' || kind create cluster --name '${self.triggers.cluster_name}' --config '${self.triggers.kind_config_path}'"
  }

  provisioner "local-exec" {
    when    = destroy
    command = "kind delete cluster --name '${self.triggers.cluster_name}'"
  }
}

# Postgres + NATS for local dev (go run ./cmd/... against localhost). The
# in-cluster copies the Helm chart manages are separate and unaffected by
# this — see deploy/helm.
resource "null_resource" "dev_services" {
  depends_on = [null_resource.kind_cluster]

  triggers = {
    compose_file = abspath("${path.module}/../../docker-compose.yaml")
  }

  provisioner "local-exec" {
    command = "docker compose -f '${self.triggers.compose_file}' --project-directory '${dirname(self.triggers.compose_file)}' up -d"
  }

  provisioner "local-exec" {
    when    = destroy
    command = "docker compose -f '${self.triggers.compose_file}' --project-directory '${dirname(self.triggers.compose_file)}' down"
  }
}
