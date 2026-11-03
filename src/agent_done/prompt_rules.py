"""Local prompt commands consumed by UserPromptSubmit before inference."""

# Also used by the launcher to recognize the hook's blocked-turn feedback.
CLIENT_EXIT_REASON = "agent-done:client_exit"
RULES = {":q": "client_exit"}


def prompt_result(event):
    """Only a complete, exact command is consumed; ordinary prompts pass through."""
    if event.get("hook_event_name") != "UserPromptSubmit":
        return {}
    prompt = event.get("prompt")
    if isinstance(prompt, str) and RULES.get(prompt) == "client_exit":
        return {"decision": "block", "reason": CLIENT_EXIT_REASON}
    return {}
