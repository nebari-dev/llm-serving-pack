"""Generate the shared provider resource from explicitly selected catalog IDs."""

from copy import deepcopy


def manifest(provider_spec, name, namespace, groups, model_ids, catalog):
    known = {m["modelId"] for m in catalog}
    missing = set(model_ids) - known
    if missing:
        raise ValueError(
            "targets absent from the active text catalog: " + ", ".join(sorted(missing))
        )
    return {
        "apiVersion": "llm.nebari.dev/v1alpha1",
        "kind": "PassthroughModel",
        "metadata": {"name": name, "namespace": namespace},
        "spec": {
            "provider": deepcopy(provider_spec),
            "models": {"declared": sorted(set(model_ids))},
            "access": {"groups": list(groups)},
        },
    }
