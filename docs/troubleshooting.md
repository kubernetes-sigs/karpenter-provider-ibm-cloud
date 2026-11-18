# Troubleshooting Guide

This guide helps diagnose and resolve common issues with the Karpenter IBM Cloud Provider.

The examples below use release `karpenter-ibm`; substitute the installed chart fullname when using another release name.

## Quick Diagnostics

### Check Controller Status
```bash
# Check if Karpenter controller is running
kubectl get pods -n karpenter

# Check controller logs
kubectl logs -n karpenter deployment/karpenter-ibm -f

# Check controller startup messages
kubectl logs -n karpenter deployment/karpenter-ibm | grep "Starting Controller"
```

## Common Issues

### Upgrade and Cleanup Recovery

Install the matching chart RBAC and CRDs before deploying a new image. Older controllers may require their original RBAC when restored. New launch, IKS allocation/cleanup, load-balancer target, hash-migration, and placement records use version 1/minimum-writer version 1. Hash version 2 migrates unchanged legacy claims without inducing drift. Older binaries must not manage remaining records they cannot read. Retire those allocations and reservations before downgrading.

For legacy IKS workers, retain the original Node, claim/class/cluster UIDs, account, region, cluster/pool/worker IDs, provider ID, and zone. Verify these against the original Node inventory and a fresh cloud worker lookup. The controller can prepare `karpenter-ibm.sh/iks-legacy-retirement` from intact ownership evidence; stranded claims require an administrator-pinned original identity record with `version`/`minimumWriterVersion` 1. Patch only the saved claim UID/resourceVersion, preserve other annotations, then delete the claim normally. Cleanup removes only the exact worker; never resize/delete its shared pool. Missing evidence leaves cleanup blocked.

Legacy load-balancer registration needs an original target snapshot, not targets from an edited class. Restore access to the original account before retrying cleanup; do not strip finalizers. Disaster recovery requires a backup that preserves the `kube-system`, class, claim, and Node UIDs. Recreating manifests produces new ownership identities and cannot adopt the old allocations automatically.

Bootstrap metadata failures are recorded in `kube-system/karpenter-bootstrap-<claim UID>` and `BootstrapFailed` claim Events. The record contains status, not token credentials. Check the worker's trusted CA and API connectivity before changing CSR policy; generic provider-token `nodeclient` autoapproval must remain disabled.

### Authentication Issues

!!! failure "Failed to authenticate with IBM Cloud API"
    ```
    Failed to create VPC client: authentication failed
    Error: {"errorMessage":"Unauthorized","errorCode":"401"}
    ```

    **Solution:**

    1. Verify API keys are correctly set
    2. Check Service ID permissions
    3. Update your values file's `credentials.ibmApiKey`, `credentials.vpcApiKey`, and `credentials.region`, then rotate through Helm:
    ```bash
    helm upgrade karpenter-ibm karpenter-ibm/karpenter-ibm -n karpenter -f credentials.yaml
    kubectl rollout status deployment/karpenter-ibm -n karpenter
    ```
    The Secret is `<fullname>-credentials`, using `ibm_api_key`, `vpc_api_key`, `region`, and optional `account_id`, `zone`, `resource_group_id`, `vpc_url`, and `vpc_auth_type`. A direct Secret edit must preserve the other keys and be followed by a controller restart.

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

!!! error "Nodes not joining cluster after provisioning"
    This is often caused by a chain of issues. Work through this systematic checklist:

#### 1. Verify Instance Creation

```bash
# Check if instances are being created
kubectl get nodeclaim NODECLAIM_NAME -o jsonpath='{.status.providerID}'
ibmcloud is instance INSTANCE_ID --output json

# Check NodeClaim status
kubectl get nodeclaims -o wide
kubectl describe nodeclaim NODECLAIM_NAME
```

**Expected:** Instance status `running`, NodeClaim shows `Launched: True`

#### 2. Check Network Connectivity (Most Common Issue)

**Step 2a: Verify Subnet Placement**
```bash
# Find which subnet your cluster nodes are in
kubectl get nodes -o wide  # Note the INTERNAL-IP range

# Check if Karpenter nodes are in the same subnet
ibmcloud is instance INSTANCE_ID --output json | jq '.primary_network_interface.subnet'

# If different subnets, nodes may be network-isolated!
```

**Step 2b: Verify API Server Endpoint Configuration**
```bash
# Find the INTERNAL API endpoint (not external!)
kubectl get endpoints kubernetes -o yaml
# OR
kubectl get endpointslice -n default -l kubernetes.io/service-name=kubernetes

# Check what's configured in IBMNodeClass
kubectl get ibmnodeclass YOUR-NODECLASS -o yaml | grep apiServerEndpoint

# Update if using external IP instead of internal
kubectl patch ibmnodeclass YOUR-NODECLASS --type='merge' \
  -p='{"spec":{"apiServerEndpoint":"https://INTERNAL-IP:6443"}}'
```

**Step 2c: Test Connectivity from Node**
```bash
# Attach floating IP for debugging

# Then SSH and test
ssh -i ~/.ssh/eb root@FLOATING_IP

# Test network layers
ping INTERNAL_API_IP                          # Test ICMP
telnet INTERNAL_API_IP 6443                   # Test TCP
curl --cacert /etc/kubernetes/pki/ca.crt https://INTERNAL_API_IP:6443/healthz
```

#### 3. Verify Security Groups

!!! danger "Security Group Requirements"
    Both worker and control plane security groups need proper rules for bidirectional communication.

**Required Security Group Rules:**

```bash
# Check current security groups on instance
ibmcloud is instance INSTANCE_ID --output json | \
  jq '.network_interfaces[0].security_groups'

# Worker Node Security Group needs:
# Outbound rules
- TCP 6443 to control plane subnet (Kubernetes API)
- TCP 10250 to all nodes (Kubelet)
- TCP/UDP 53 to 0.0.0.0/0 (DNS)
- TCP 80,443 to 0.0.0.0/0 (Package downloads)

# Inbound rules
- TCP 6443 from control plane (API server callbacks)
- TCP 10250 from all nodes (Kubelet peer communication)

# Add missing rules example:
ibmcloud is security-group-rule-add WORKER_SG_ID \
  outbound tcp --port-min 6443 --port-max 6443 \
  --remote CONTROL_PLANE_SUBNET_CIDR

ibmcloud is security-group-rule-add WORKER_SG_ID \
  inbound tcp --port-min 6443 --port-max 6443 \
  --remote CONTROL_PLANE_SUBNET_CIDR
```

#### 4. Debug Bootstrap Process

**Check Cloud-Init Status:**
```bash
# SSH to node (after attaching floating IP)
ssh -i ~/.ssh/eb root@FLOATING_IP

# Check cloud-init progress
sudo cloud-init status --long

# View bootstrap logs
sudo tail -100 /var/log/cloud-init.log
sudo tail -100 /var/log/cloud-init-output.log
sudo cat /var/log/karpenter-bootstrap.log

# Check if kubelet was installed
sudo systemctl status kubelet
sudo journalctl -u kubelet --no-pager -n 50
```

**Common Bootstrap Issues:**
- Package repository access blocked (check security groups for HTTP/HTTPS)
- CNI conflicts (check for pre-existing CNI configurations)

#### 5. Verify IBMNodeClass Configuration

```bash
# Check for common configuration issues
kubectl get ibmnodeclass YOUR-NODECLASS -o yaml

# Key fields to verify:
# - apiServerEndpoint: Must be INTERNAL cluster endpoint
# - bootstrapMode: Should be "cloud-init" for VPC
# - securityGroups: Must include proper security group IDs
# - sshKeys: Must use SSH key IDs (r010-xxx format), not names
```

#### 6. Check Resource Group Configuration

```bash
# Verify instances are created in correct resource group
ibmcloud is instances --output json | \
  jq '.[] | select(.name | contains("nodepool")) |
  {name: .name, resource_group: .resource_group.id}'

# Should match the resource group in IBMNodeClass
kubectl get ibmnodeclass YOUR-NODECLASS -o yaml | grep resourceGroup
```

### Security Group Configuration

!!! warning "Kubernetes API Server Access"
    **Common Issue:** Security groups blocking API server communication (TCP 6443)

    **Symptoms:**
    ```bash
    # From worker node:
    ping API_SERVER_IP              # SUCCESS
    curl https://API_SERVER_IP:6443 # TIMEOUT
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
    curl --cacert /etc/kubernetes/pki/ca.crt https://API_SERVER_IP:6443/healthz
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
        - name: LOG_LEVEL
          value: debug
```

## Getting Help

- [GitHub Issues](https://github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/issues)
