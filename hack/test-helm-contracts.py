# Copyright The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import pathlib
import subprocess

import jsonschema
import yaml

ROOT = pathlib.Path(__file__).resolve().parents[1]


def render(extra=(), successful=True):
    command = ["helm", "template", "audit", str(ROOT / "charts"), "--namespace", "operators",
               "--set", "credentials.ibmApiKey=placeholder", "--set", "credentials.vpcApiKey=placeholder",
               "--set", "credentials.region=us-south", *extra]
    result = subprocess.run(command, capture_output=True, text=True)
    assert (result.returncode == 0) == successful, result.stderr
    return [obj for obj in yaml.safe_load_all(result.stdout) if obj] if successful else result.stderr


def deployment(objects):
    return next(obj for obj in objects if obj["kind"] == "Deployment")


def strict_schema(node):
    if isinstance(node, list):
        return [strict_schema(value) for value in node]
    if not isinstance(node, dict):
        return node
    node = {key: strict_schema(value) for key, value in node.items() if not key.startswith("x-kubernetes-")}
    if node.get("type") == "object" and "properties" in node and "additionalProperties" not in node:
        node["additionalProperties"] = False
    return node


crd = yaml.safe_load((ROOT / "charts/crds/karpenter-ibm.sh_ibmnodeclasses.yaml").read_text())
spec_schema = strict_schema(crd["spec"]["versions"][0]["schema"]["openAPIV3Schema"]["properties"]["spec"])
for mode in ("vpc", "iks"):
    objects = render(["-f", str(ROOT / f"charts/examples/{mode}-example-values.yaml")])
    node_class = next(obj for obj in objects if obj["kind"] == "IBMNodeClass")
    jsonschema.validate(node_class["spec"], spec_schema)
    env = deployment(objects)["spec"]["template"]["spec"]["containers"][0]["env"]
    names = [entry["name"] for entry in env]
    assert len(names) == len(set(names)), names
    assert "image" not in node_class["spec"] if mode == "iks" else "imageSelector" in node_class["spec"]

render(["--set", "customResources.enabled=true", "--set", "customResources.nodeClass.vpc.vpcId=legacy"], False)
render(["--set", "bootstrapMode=invalid"], False)
render(["--set", "customResources.enabled=true"], False)
render(["--set", "credentials.ibm_api_key=ignored"], False)
baseline = deployment(render())["spec"]["template"]["metadata"]["annotations"]
for override, checksum in [
    ("credentials.ibmApiKey=rotated", "checksum/credentials"),
    ("circuitBreaker.presets.balanced.failureThreshold=7", "checksum/circuit-breaker"),
    ("additionalCAs.enabled=true", "checksum/additional-ca"),
]:
    rotated = deployment(render(["--set", override]))["spec"]["template"]["metadata"]["annotations"]
    assert baseline[checksum] != rotated[checksum], override
    for other in set(baseline) - {checksum}:
        assert baseline[other] == rotated[other], (override, other)
print("Helm schema, mode, environment and rotation contracts passed")
