# Server business LLM configuration

These four files are the checked-in, secret-free configuration templates for
the server-side LLM businesses. `docker-compose.yml` mounts this directory at
`/app/config/llm` and enables the registry for the backend.

`llm.api_key_env` is only the name of an environment variable; the key itself
must stay in `docker/.env` or another deployment secret store. The
`llm.base_url` values use `${MULTICA_LLM_BASE_URL}`, which the server expands
when loading or reloading a file. Edit the model, limits, prompts, or
`enabled` flag per business as needed. Changes are picked up by the backend's
poller without rebuilding the image.

The files intentionally share `MULTICA_LLM_API_KEY` for the default Docker
deployment. Use a separate environment variable name in a business file when
different credentials are required.
