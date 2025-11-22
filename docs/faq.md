# Frequently Asked Questions

## How does Karpenter make sizing decisions for matching pending workloads with instance types?

Karpenter uses amulti-step process to match pending pods with the most appropriate IBM Cloud instance types:

### 1. Instance Type Filtering
Karpenter first filters available instance types based on pod requirements:
- **Architecture requirements** (e.g., amd64, arm64)
- **Minimum CPU and memory** requirements
- **Maximum hourly price** constraints (if specified)
- **Zone availability** in your region

### 2. Cost-Efficiency Ranking
Each viable instance type receives a cost-efficiency score calculated as:
```
score = (price_per_CPU + price_per_GB_memory) / 2
```
Lower scores indicate better cost efficiency. When pricing data is unavailable, Karpenter falls back to resource-based ranking, preferring smaller instances.

### 3. Bin Packing Optimization
The scheduler attempts to:
- Pack multiple pending pods onto the smallest/cheapest instance that can accommodate them
- Respect pod affinities, anti-affinities, and topology spread constraints
- Honor taints/tolerations and node selectors
- Account for system overhead and daemon resources

## How does Karpenter handle multi-zone deployments?

Karpenter automatically discovers available zones in your IBM Cloud region and creates instance offerings across all zones.

## What happens if pricing information is unavailable?

If the pricing provider cannot retrieve pricing data for an instance type, Karpenter falls back to resource-based ranking. It will prefer smaller instances (lower combined CPU + memory count) to ensure workloads can still be scheduled efficiently even without cost data.

## How does Karpenter decide when to provision a new node?

Karpenter watches for pods that cannot be scheduled due to insufficient resources. When it detects pending pods:
1. It evaluates if existing nodes can accommodate them
2. If not, it calculates the optimal new node configuration
3. It provisions the smallest/cheapest instance type that satisfies all requirements

## Can I restrict which instance types Karpenter uses?

Yes, you can control instance type selection through your NodePool configuration:
- Use `instanceTypes` to explicitly list allowed instance types
- Set `minCPU`, `minMemory`, and `maxPrice` requirements
- Use taints and node selectors to influence placement

## How does Karpenter handle DaemonSets?

Karpenter automatically accounts for DaemonSet resource requirements when calculating available capacity on nodes. It ensures that nodes have sufficient resources for both DaemonSets and regular pods.

## What's the difference between VPC and IKS modes?

- **VPC Mode**: Used for self-managed Kubernetes clusters on IBM Cloud VPC. Karpenter directly provisions Virtual Server Instances and manages the complete node lifecycle.
- **IKS Mode**: Used with IBM Kubernetes Service. Karpenter scales existing worker pools rather than creating individual instances.

## Does Karpenter support GPU instances?

Yes, Karpenter recognizes and properly accounts for GPU resources in IBM Cloud instance types. It will match pods requesting GPU resources with appropriate GPU-enabled instances.

## How does node consolidation work?

Karpenter continuously evaluates node utilization and automatically:
- Removes nodes that are empty or underutilized
- Consolidates workloads onto fewer nodes when possible
- Respects PodDisruptionBudgets during consolidation
