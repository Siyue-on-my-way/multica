# Server business LLM configuration

These four files are the checked-in, secret-free configuration templates for
the server-side LLM businesses. `docker-compose.yml` mounts this directory at
`/app/config/llm` and enables the registry for the backend.

`llm.api_key_env` is only the name of an environment variable. Prefer it for
shared deployments so the key itself stays in `docker/.env` or another secret
store. For local or isolated gateways, `llm.api_key` may contain the literal
key instead; configure exactly one of these two fields. The `llm.base_url`
values use `${MULTICA_LLM_BASE_URL}`, which the server expands when loading or
reloading a file. Edit the model, limits, prompts, or `enabled` flag per
business as needed. Changes are picked up by the backend's poller without
rebuilding the image.

The files intentionally share `MULTICA_LLM_API_KEY` for the default Docker
deployment. Use a separate environment variable name in a business file when
different credentials are required.

`subissue-suggest.yaml` contains two independently configurable stages in the
same business file: `outline` returns lightweight alternative title/goal plans,
and `detail` expands only the outline approved by the user. They may use
different models, endpoints, credentials, prompts, and token/time budgets.
