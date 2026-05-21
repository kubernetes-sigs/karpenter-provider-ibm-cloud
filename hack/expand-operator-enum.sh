#!/bin/bash
set -eu -o pipefail

# Expand the NodeSelectorRequirement operator enum in karpenter CRDs to
# the full set declared in the docstring (In, NotIn, Exists, DoesNotExist,
# Gt, Lt, Gte, Lte).
#
# Upstream karpenter declares the marker as
#   +kubebuilder:validation:Enum:=Gte;Lte
# with a comment claiming controller-gen will union the values with the
# base v1.NodeSelectorOperator enum. The union does not happen, so the
# generated CRDs reject every In/NotIn/Exists/... selector that
# karpenter itself produces. Patch the generated YAML in place until the
# upstream marker is fixed.

CHARTS_DIR="${1:-charts/crds}"

python3 - <<PY
import re, sys, pathlib
charts = pathlib.Path("$CHARTS_DIR")
targets = [
    "karpenter.sh_nodeclaims.yaml",
    "karpenter.sh_nodepools.yaml",
    "karpenter.sh_nodeoverlays.yaml",
]
ops = ["In", "NotIn", "Exists", "DoesNotExist", "Gt", "Lt", "Gte", "Lte"]
pattern = re.compile(r"(?P<indent>[ ]+)enum:\n(?P=indent)- Gte\n(?P=indent)- Lte\n")

def replace(m):
    indent = m.group("indent")
    return f"{indent}enum:\n" + "".join(f"{indent}- {op}\n" for op in ops)

for name in targets:
    path = charts / name
    if not path.exists():
        continue
    text = path.read_text()
    new = pattern.sub(replace, text)
    if new != text:
        path.write_text(new)
        print(f"expanded operator enum in {path}")
PY
