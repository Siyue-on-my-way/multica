# Server business LLM configuration

These four files are the checked-in, secret-free configuration templates for
the server-side LLM businesses. `docker-compose.yml` mounts this directory at
`/app/config/llm` and enables the registry for the backend.

`llm.api_key_env` is only the name of an environment variable. Prefer it for
shared deployments so the key itself stays in `docker/.env` or another secret
store. For local or isolated gateways, `llm.api_key` may contain the literal
key instead; configure exactly one of these two fields. The `llm.base_url`
values use `${MULTICA_LLM_BASE_URL}`, which the server expands when loading or
reloading a file. `llm.reasoning_effort` (one of `none`, `minimal`, `low`,
`medium`, `high`, `xhigh`) is sent as `reasoning_effort` on every request;
when it is unset, only the GPT-5.6 family gets a built-in reasoning setting
(`none`) and every other model gets none. Edit the model, limits, prompts, or
`enabled` flag per business as needed. Changes are picked up by the backend's
poller without rebuilding the image.

The files intentionally share `MULTICA_LLM_API_KEY` for the default Docker
deployment. Use a separate environment variable name in a business file when
different credentials are required.

`subissue-suggest.yaml` contains five stages in the same business file. The
shared `recognize` stage uses `gemini-3.8-flash` with an 8,192-token output
limit to build one authoritative task list. The three independent strategy
stages run concurrently, in this order:

* `single_full_context`: one issue containing the complete context.
* `parallel`: split only work that has no real dependency and can be started
  independently.
* `full_coverage`: preserve the clearest task boundaries while covering every
  recognized task.

All three strategy stages use `gemini-3.1-pro-preview`, `high` reasoning,
temperature `0.4`, and an 8,192-token output limit. `detail` uses the same
model with temperature `0.3` and a 16,384-token output limit after the user
approves one outline. These limits are upper bounds, not guaranteed output
lengths: the preview panel shows each call's actual character/token counts,
model, prompts, and raw JSON so the three strategies can be compared before
creating issues. The old `outline` stage remains accepted for deployments
that have not migrated their YAML yet.
