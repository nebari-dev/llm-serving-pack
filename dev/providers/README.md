# Provider discovery and verification

Run `provider.py --help`, or `provider.py --backend bedrock --help` for AWS options.
Install dependencies in a virtual environment with `pip install -r requirements.txt`.

`discover` reads the provider's catalog. `manifest` emits a resource for selected
IDs. Neither invokes models or applies resources. `verify` makes bounded native
SDK requests and runs the shared streaming and access-control checks when both
gateway URLs and credentials are supplied.

See [provider integrations](../../docs/src/content/docs/providers.mdx) for the
adapter interface, gateway credentials, and follow-up provider checklist, and
[Bedrock](../../docs/src/content/docs/bedrock.mdx) for AWS-specific setup.

Run credential-free coverage from the repository root:

```sh
python -m unittest discover -s dev/providers -v
```
