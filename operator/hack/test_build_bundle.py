import argparse
from datetime import date
import json
from pathlib import Path
import tempfile
import unittest

import yaml
from build_bundle import generate, ROOT, KEYS


class BundleTests(unittest.TestCase):
    def test_operator_can_grant_operand_roles_without_unscoped_escalation(self):
        documents = list(yaml.safe_load_all((ROOT / "config/rbac.yaml").read_text()))
        rules = next(obj["rules"] for obj in documents if obj["kind"] == "ClusterRole")
        # Kubernetes CREATE authorization has no resource name. A named
        # 'escalate' grant cannot authorize creation of the operand roles.
        # The Operator instead explicitly holds the permissions it delegates.
        for rule in rules:
            self.assertNotIn("escalate", rule["verbs"])
        operands = json.loads((ROOT / "internal/operator/operands.json").read_text())
        for operand in operands:
            if operand["kind"] == "ClusterRole":
                for delegated in operand["rules"]:
                    self.assertTrue(any(
                        set(delegated["apiGroups"]) <= set(rule["apiGroups"])
                        and set(delegated["resources"]) <= set(rule["resources"])
                        and set(delegated["verbs"]) <= set(rule["verbs"])
                        and not rule.get("resourceNames")
                        for rule in rules), delegated)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.args = argparse.Namespace(
            version="0.1.0", operator_image="example.invalid/operator:dev",
            bundle_image="example.invalid/bundle:dev", images=ROOT / "config/development-images.json",
            output=Path(self.temp.name) / "bundle", release=False, openshift_versions=None,
            created_at=date.today().isoformat(), support="Hammerspace", channel="alpha",
            valid_subscription="A valid Hammerspace software subscription is required.")

    def test_inventory_matches_deployed_images_and_permissions(self):
        generate(self.args)
        csv = yaml.safe_load((self.args.output / "manifests/hammerspace-csi-operator.clusterserviceversion.yaml").read_text())
        images = {entry["name"]: entry["image"] for entry in csv["spec"]["relatedImages"]}
        self.assertEqual(set(images), KEYS | {"operator"})
        deployment = csv["spec"]["install"]["spec"]["deployments"][0]["spec"]
        manager = deployment["template"]["spec"]["containers"][0]
        self.assertEqual(manager["image"], images["operator"])
        for env in manager["env"]:
            if env["name"].startswith("RELATED_IMAGE_"):
                self.assertEqual(env["value"], images[env["name"][len("RELATED_IMAGE_"):].lower()])
        original = list(yaml.safe_load_all((ROOT / "config/rbac.yaml").read_text()))
        for kind, field in [("Role", "permissions"), ("ClusterRole", "clusterPermissions")]:
            expected = next(obj["rules"] for obj in original if obj["kind"] == kind)
            self.assertEqual(csv["spec"]["install"]["spec"][field][0]["rules"], expected)
        self.assertEqual([m["type"] for m in csv["spec"]["installModes"] if m["supported"]], ["OwnNamespace"])
        self.assertEqual(csv["spec"]["minKubeVersion"], "1.29.0")
        catalog = [json.loads(line) for line in (self.args.output / "catalog.json").read_text().splitlines()]
        self.assertEqual(catalog[2]["name"], csv["metadata"]["name"])
        self.assertEqual(catalog[2]["image"], self.args.bundle_image)
        self.assertEqual(catalog[2]["relatedImages"], csv["spec"]["relatedImages"])
        annotations = csv["metadata"]["annotations"]
        for key in ["categories", "description", "containerImage", "createdAt", "support",
                    "operators.openshift.io/valid-subscription",
                    "features.operators.openshift.io/disconnected",
                    "features.operators.openshift.io/fips-compliant",
                    "features.operators.openshift.io/proxy-aware",
                    "features.operators.openshift.io/tls-profiles",
                    "features.operators.openshift.io/token-auth-aws",
                    "features.operators.openshift.io/token-auth-azure",
                    "features.operators.openshift.io/token-auth-gcp",
                    "features.operators.openshift.io/csi"]:
            self.assertIn(key, annotations)
        template = json.loads(annotations["operatorframework.io/suggested-namespace-template"])
        self.assertEqual(template["kind"], "Namespace")
        self.assertEqual(template["metadata"]["name"], annotations["operatorframework.io/suggested-namespace"])
        self.assertEqual(template["metadata"]["labels"]["pod-security.kubernetes.io/enforce"], "privileged")

    def test_release_requires_digests_and_explicit_range(self):
        self.args.release = True
        with self.assertRaises(ValueError):
            generate(self.args)
        self.args.openshift_versions = "v4.22"
        with self.assertRaises(ValueError):
            generate(self.args)
        pinned = {k: "example.invalid/{}@sha256:{}".format(k, "a" * 64) for k in KEYS}
        self.args.images = Path(self.temp.name) / "images.json"
        self.args.images.write_text(json.dumps(pinned))
        self.args.operator_image = "example.invalid/operator@sha256:" + "b" * 64
        generate(self.args)
        csv = yaml.safe_load((self.args.output / "manifests/hammerspace-csi-operator.clusterserviceversion.yaml").read_text())
        self.assertEqual(csv["metadata"]["annotations"]["com.redhat.openshift.versions"], "v4.22")
        self.assertEqual(csv["metadata"]["annotations"]["features.operators.openshift.io/disconnected"], "true")
        self.assertNotIn("Development packaging", csv["spec"]["description"])
        metadata = yaml.safe_load((self.args.output / "metadata/annotations.yaml").read_text())
        self.assertEqual(metadata["annotations"]["com.redhat.openshift.versions"], "v4.22")
        self.assertIn('LABEL "com.redhat.openshift.versions"="v4.22"', (self.args.output / "Dockerfile").read_text())

    def test_release_accepts_digest_pinned_upstream_sidecars(self):
        self.args.release = True
        self.args.openshift_versions = "v4.22"
        pinned = {k: "registry.k8s.io/sig-storage/{}@sha256:{}".format(k, "a" * 64) for k in KEYS}
        self.args.images = Path(self.temp.name) / "images.json"
        self.args.images.write_text(json.dumps(pinned))
        self.args.operator_image = "example.invalid/operator@sha256:" + "b" * 64
        generate(self.args)
        csv = yaml.safe_load((self.args.output / "manifests/hammerspace-csi-operator.clusterserviceversion.yaml").read_text())
        related = {item["name"]: item["image"] for item in csv["spec"]["relatedImages"]}
        for name in KEYS:
            self.assertEqual(related[name], pinned[name])

    def test_rejects_incomplete_image_inventory(self):
        self.args.images = Path(self.temp.name) / "images.json"
        self.args.images.write_text(json.dumps({"driver": "example.invalid/driver:dev"}))
        with self.assertRaises(ValueError):
            generate(self.args)


    def test_channel_sets_bundle_catalog_and_maturity(self):
        self.args.channel = "stable"
        generate(self.args)
        csv = yaml.safe_load((self.args.output / "manifests/hammerspace-csi-operator.clusterserviceversion.yaml").read_text())
        self.assertEqual(csv["spec"]["maturity"], "stable")
        annotations = yaml.safe_load((self.args.output / "metadata/annotations.yaml").read_text())["annotations"]
        self.assertEqual(annotations["operators.operatorframework.io.bundle.channels.v1"], "stable")
        self.assertEqual(annotations["operators.operatorframework.io.bundle.channel.default.v1"], "stable")
        catalog = [json.loads(line) for line in (self.args.output / "catalog.json").read_text().splitlines()]
        self.assertEqual(catalog[0]["defaultChannel"], "stable")
        self.assertEqual(catalog[1]["name"], "stable")

if __name__ == "__main__":
    unittest.main()
