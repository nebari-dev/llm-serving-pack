"""Entrypoint for the model-downloader container.

Invokes the Hugging Face Hub CLI download command. All arguments
are passed through to `hf download`.
"""

import sys

from huggingface_hub.cli.hf import main

sys.argv[0] = "hf"
sys.exit(main())
