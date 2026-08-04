output "kubeconfig_context" {
  description = "kubectl context to use after apply"
  value       = "kind-${var.cluster_name}"
}
