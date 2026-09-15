"""Generated manifests must satisfy the shipped PassthroughModel CRD schema."""

import unittest
from pathlib import Path
from types import SimpleNamespace

import yaml

from backends.bedrock import Bedrock
from catalog import manifest

CRD = (
    Path(__file__).resolve().parents[2]
    / "charts/nebari-llm-serving/crds/passthroughmodel-crd.yaml"
)
TYPES = {"object": dict, "array": list, "string": str, "boolean": bool, "integer": int}


def schema_errors(instance, schema, path):
    """Return structural mismatches against an openAPIV3Schema fragment.

    Checks type, required, enum, and declared properties: enough to catch
    values the API server would reject and fields it would silently prune.
    """
    kind = schema.get("type")
    if kind and not isinstance(instance, TYPES[kind]):
        return [f"{path}: expected {kind}, got {type(instance).__name__}"]
    errors = []
    if "enum" in schema and instance not in schema["enum"]:
        errors.append(f"{path}: {instance!r} not in enum {schema['enum']}")
    if kind == "object" and "properties" in schema:
        properties = schema["properties"]
        for key in schema.get("required", []):
            if key not in instance:
                errors.append(f"{path}.{key}: required property missing")
        for key, value in instance.items():
            if key not in properties:
                errors.append(f"{path}.{key}: not declared in the CRD schema")
            else:
                errors += schema_errors(value, properties[key], f"{path}.{key}")
    if kind == "array":
        for index, item in enumerate(instance):
            errors += schema_errors(item, schema.get("items", {}), f"{path}[{index}]")
    return errors


class ManifestSchemaTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        crd = yaml.safe_load(CRD.read_text())
        (version,) = crd["spec"]["versions"]
        cls.api_version = crd["spec"]["group"] + "/" + version["name"]
        cls.kind = crd["spec"]["names"]["kind"]
        cls.schema = version["schema"]["openAPIV3Schema"]

    def resource_errors(self, resource):
        self.assertEqual(resource["apiVersion"], self.api_version)
        self.assertEqual(resource["kind"], self.kind)
        return schema_errors(resource, self.schema, self.kind)

    def test_bedrock_manifest_satisfies_the_shipped_crd(self):
        adapter = Bedrock(
            SimpleNamespace(region="us-west-2", profile=None, publisher=[])
        )
        resource = manifest(
            adapter.provider_spec(),
            "bedrock",
            "nebari-llm-serving-system",
            ["team"],
            ["amazon.nova-lite-v1:0"],
            [{"modelId": "amazon.nova-lite-v1:0"}],
        )
        self.assertEqual(self.resource_errors(resource), [])

    def test_schema_check_rejects_backend_types_absent_from_the_crd_enum(self):
        # test_provider.py's ExampleProvider shape: proof this check catches a
        # backend drifting from the shipped enum instead of passing silently.
        resource = manifest(
            {
                "backend": {"type": "Example", "example": {"tenant": "team"}},
                "credential": {"type": "WorkloadIdentity"},
            },
            "example",
            "nebari-llm-serving-system",
            ["team"],
            ["example/model-a"],
            [{"modelId": "example/model-a"}],
        )
        errors = self.resource_errors(resource)
        self.assertTrue(any("'Example' not in enum" in error for error in errors))
        self.assertTrue(
            any("backend.example: not declared" in error for error in errors)
        )


if __name__ == "__main__":
    unittest.main()
