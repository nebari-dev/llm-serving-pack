# Bedrock discovery and verification

Run `bedrock.py --help` for commands. Install dependencies in a virtual environment
with `pip install -r requirements.txt`. The [Bedrock guide](../../docs/src/content/docs/bedrock.mdx)
covers AWS permissions, workload identity, and checks through both pack endpoints.

`discover` reads AWS's current catalog and availability APIs. `manifest` emits a
Kubernetes resource for explicitly selected targets. Neither invokes models or
applies resources. `verify` makes bounded, billable Converse and ConverseStream
requests. With both gateway URLs and credentials it also checks routing and scope.

Run credential-free contract tests with:

```sh
python -m unittest discover -s dev/bedrock -v
```
