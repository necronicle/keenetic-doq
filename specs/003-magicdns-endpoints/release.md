# DOQ v0.6.7

## Requirements and success criteria

The built-in resolver must return only 109.120.187.249 for api.openai.com, chatgpt.com, gemini.google.com, portal.anthropic.com and releases.openai.com. Keep every other mapping. Publish the patch release and announce it in Telegram topic 55256 after the binaries are available.

## Implementation plan and tasks

1. Update the five exact host mappings, export expectations and snapshot documentation.
2. Verify the built-in resolver, compare all other mappings against v0.6.6 and run the Go tests.
3. Tag the tested commit, verify the GitHub release assets and send the topic announcement.

## Network evidence

On November 24 the installer redirect and release metadata succeeded through 109.120.187.249. ChatGPT and Gemini returned 200, the OpenAI models API returned 401 without a key, and the Anthropic portal redirected to login. The same five hosts timed out through 217.144.190.216 from the test network.
