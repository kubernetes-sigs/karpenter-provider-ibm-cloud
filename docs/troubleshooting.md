# Troubleshooting Guide

This guide helps diagnose and resolve common issues with the Karpenter IBM Cloud Provider.

## Quick Diagnostics

### Check Controller Status
```bash
# Check if Karpenter controller is running
kubectl get pods -n karpenter

# Check controller logs
kubectl logs -n karpenter deployment/karpenter -f

# Check controller startup messages
kubectl logs -n karpenter deployment/karpenter | grep "Starting Controller"
```

## Common Issues

### Authentication Issues

!!! failure "Failed to authenticate with IBM Cloud API"
    ```
    Failed to create VPC client: authentication failed
    Error: {"errorMessage":"Unauthorized","errorCode":"401"}
    ```

    **Solution:**

    1. Verify API keys are correctly set
    2. Check Service ID permissions
    3. Update Kubernetes secret:
    ```bash
    kubectl create secret generic karpenter-ibm-credentials \
      --from-literal=api-key="$IBM_API_KEY" \
      --from-literal=vpc-api-key="$VPC_API_KEY" \
      --namespace karpenter --dry-run=client -o yaml | kubectl apply -f -
    ```

### Instance Provisioning Issues

!!! warning "No suitable subnets found"
    **Diagnosis:**
    ```bash
    # Check available subnets
    ibmcloud is subnets --output json

    # Check subnet capacity
    ibmcloud is subnet SUBNET_ID --output json
    ```

    **Solutions:**

    - Verify subnet exists in specified zone
    - Ensure subnet has available IP addresses
    - Consider using auto-subnet selection

### Node Registration Issues

!!! error "Node failed to join cluster"
    **Most Common Issue - Wrong API Server Endpoint:**
    ```bash
    # Symptoms: kubelet timeouts, nodes never register
    # Error: "dial tcp 10.243.65.4:6443: i/o timeout"

    # 1. Check what endpoint kubelet is trying to reach
    ssh ubuntu@INSTANCE_IP "cat /var/lib/kubelet/bootstrap-kubeconfig | grep server"

    # 2. Find correct internal API endpoint
    kubectl get endpointslice -n default -l kubernetes.io/service-name=kubernetes

    # 3. Update NodeClass with correct INTERNAL endpoint
    kubectl patch ibmnodeclass YOUR-NODECLASS --type='merge' \
      -p='{"spec":{"apiServerEndpoint":"https://INTERNAL-IP:6443"}}'
    ```

    **Other Common Causes:**

    - VNI (Virtual Network Interface) not configured properly (v0.3.53+ required)
    - Bootstrap token expiration
    - Network connectivity problems

    **Debug steps:**
    ```bash
    # Check bootstrap logs on instance
    ssh ubuntu@INSTANCE_IP "sudo journalctl -u cloud-final"

    # Check kubelet status and errors
    ssh ubuntu@INSTANCE_IP "sudo systemctl status kubelet"
    ssh ubuntu@INSTANCE_IP "sudo journalctl -u kubelet --no-pager -n 50"

    # Test API server connectivity from node
    ssh ubuntu@INSTANCE_IP "curl -k -m 10 https://API-SERVER-IP:6443/healthz"
    ```

### Security Group Configuration

!!! warning "Kubernetes API Server Access"
    **Common Issue:** Security groups blocking API server communication (TCP 6443)

    **Symptoms:**
    ```bash
    # From worker node:
    ping API_SERVER_IP              # ✅ SUCCESS
    curl https://API_SERVER_IP:6443 # ❌ TIMEOUT
    ```

    **Required Security Group Rules:**

    **Worker Node Security Group:**
    ```bash
    # Allow outbound to API server
    ibmcloud is security-group-rule-add WORKER_SG_ID \
      outbound tcp --port-min 6443 --port-max 6443 \
      --remote CONTROL_PLANE_SUBNET_CIDR

    # Allow inbound for return traffic
    ibmcloud is security-group-rule-add WORKER_SG_ID \
      inbound tcp --port-min 6443 --port-max 6443 \
      --remote CONTROL_PLANE_SUBNET_CIDR
    ```

    **Control Plane Security Group:**
    ```bash
    # Allow inbound from workers
    ibmcloud is security-group-rule-add CONTROL_PLANE_SG_ID \
      inbound tcp --port-min 6443 --port-max 6443 \
      --remote WORKER_SUBNET_CIDR
    ```

    **Debug connectivity:**
    ```bash
    # Test layer by layer
    ping API_SERVER_IP                    # ICMP connectivity
    telnet API_SERVER_IP 6443            # TCP connectivity
    curl -k https://API_SERVER_IP:6443/healthz  # Application layer
    ```
## Debug Mode

Enable debug logging for detailed information:

```yaml
apiVersion: v1
kind: Deployment
metadata:
  name: karpenter
  namespace: karpenter
spec:
  template:
    spec:
      containers:
      - name: controller
        env:
        - name: KARPENTER_LOG_LEVEL
          value: debug
```

## Getting Help

- [GitHub Issues](https://github.com/pfeifferj/karpenter-provider-ibm-cloud/issues)
