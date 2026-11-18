#!/usr/bin/env python3
# Copyright The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import pathlib
import subprocess

import yaml


def verify(release, namespace, overrides):
    chart = pathlib.Path(__file__).resolve().parents[1] / "charts"
    command = [
        "helm", "template", release, str(chart), "--namespace", namespace,
        "--set", "credentials.ibmApiKey=test-placeholder",
        "--set", "credentials.vpcApiKey=test-placeholder",
        "--set", "credentials.region=us-south",
    ]
    for key, value in overrides.items():
        command.extend(["--set", f"{key}={value}"])
    for template in ["role", "rolebinding", "clusterrolebinding", "serviceaccount"]:
        command.extend(["--show-only", f"templates/{template}.yaml"])
    rendered = subprocess.run(command, check=True, text=True, capture_output=True).stdout
    objects = [obj for obj in yaml.safe_load_all(rendered) if obj]
    identities = set()
    for obj in objects:
        kind = obj["kind"]
        scoped_namespace = "" if kind.startswith("Cluster") else obj["metadata"].get("namespace", namespace)
        identity = kind, scoped_namespace, obj["metadata"]["name"]
        assert identity not in identities, f"duplicate RBAC identity: {identity}"
        identities.add(identity)
    bindings = [obj for obj in objects if obj["kind"] in {"RoleBinding", "ClusterRoleBinding"}]
    assert len(bindings) == 4, f"expected four manager bindings, got {len(bindings)}"
    for binding in bindings:
        reference = binding["roleRef"]
        target_namespace = "" if reference["kind"] == "ClusterRole" else binding["metadata"].get("namespace", namespace)
        target = reference["kind"], target_namespace, reference["name"]
        assert target in identities, f"unresolved roleRef for {release}: {target}"
        for subject in binding["subjects"]:
            if subject["kind"] == "ServiceAccount":
                target = "ServiceAccount", subject.get("namespace", namespace), subject["name"]
                assert target in identities, f"unresolved ServiceAccount for {release}: {target}"
    cluster = next(obj for obj in objects if obj["kind"] == "ClusterRole")
    assert all("secrets" not in rule.get("resources", []) for rule in cluster["rules"])
    event_rules = [rule for rule in cluster["rules"] if "events" in rule.get("resources", [])]
    assert {group for rule in event_rules for group in rule["apiGroups"]} == {"", "events.k8s.io"}
    assert all(rule["resources"] == ["events"] and set(rule["verbs"]) == {"create", "patch"} for rule in event_rules)
    system = next(obj for obj in objects if obj["kind"] == "Role" and obj["metadata"]["namespace"] == "kube-system")
    assert {"apiGroups": [""], "resources": ["secrets"], "verbs": ["create", "delete", "get", "list"]} in system["rules"]
    signers = next(rule for rule in cluster["rules"] if "signers" in rule.get("resources", []))
    assert set(signers["resourceNames"]) == {"kubernetes.io/kubelet-serving", "kubernetes.io/kube-apiserver-client-kubelet"}
    assert signers["verbs"] == ["approve"]
    assert any("subjectaccessreviews" in rule.get("resources", []) and "create" in rule["verbs"] for rule in cluster["rules"])
    assert all(subject.get("name") != "system:nodes" for binding in bindings for subject in binding["subjects"])
    print(f"RBAC references resolved: {release}, namespace={namespace}, overrides={overrides}")


for case in [
    ("karpenter-ibm", "karpenter", {}),
    ("audit", "operators", {}),
    ("team", "operators", {"nameOverride": "operator"}),
    ("alternate", "custom-namespace", {"fullnameOverride": "custom-karpenter"}),
]:
    verify(*case)
