"""Provider registry: explicit provider names only; unknown names fail closed."""
from __future__ import annotations

import os

from ..models import RouteId
from .antigravity import ANTIGRAVITY_SPEC, AntigravityProvider
from .base import BaseProvider
from .claude import CLAUDE_SPEC, ClaudeProvider
from .codex import CODEX_SPEC, CodexProvider
from .cursor import CURSOR_SPEC, CursorProvider
from .demo import DemoProvider
from .grok import GROK_SPEC, GrokProvider
from .harness import CLIHarnessProvider, HarnessSpec
from .muse import MUSE_SPEC, MuseProvider
from .openai_compatible import OpenAICompatibleProvider
from .opencode import OPENCODE_SPEC, OpenCodeProvider
from .openrouter import OpenRouterProvider

HARNESS_SPECS: tuple[HarnessSpec, ...] = (
    OPENCODE_SPEC,
    CLAUDE_SPEC,
    CODEX_SPEC,
    CURSOR_SPEC,
    GROK_SPEC,
    MUSE_SPEC,
    ANTIGRAVITY_SPEC,
)


class ProviderResolutionError(Exception):
    """A route named an unknown provider. Lists the available providers."""


def configured_routes(routes: list[dict]) -> list[dict]:
    """Routes whose own transport is configured; does not prove live authentication."""
    registry = ProviderRegistry()

    def available(route: dict) -> bool:
        name = (route.get("provider") or "").lower()
        provider = registry.get(name)
        if provider is None:
            return False
        if isinstance(provider, CLIHarnessProvider):
            return provider.is_available()
        if name == "openrouter":
            return bool(os.environ.get("OPENROUTER_API_KEY"))
        if isinstance(provider, OpenAICompatibleProvider):
            return provider.is_local or bool(provider.api_key)
        return False

    return [route for route in routes if available(route)]


class ProviderRegistry:
    def __init__(self):
        self._providers: dict[str, BaseProvider] = {}
        # Seven equal CLI-harness providers; no default preference.
        self.register("opencode", OpenCodeProvider())
        self.register("claude", ClaudeProvider())
        self.register("codex", CodexProvider())
        self.register("cursor", CursorProvider())
        self.register("grok", GrokProvider())
        self.register("muse", MuseProvider())
        self.register("antigravity", AntigravityProvider())
        self.register("openrouter", OpenRouterProvider())
        self.register("openai_compatible", OpenAICompatibleProvider(provider_name="openai_compatible"))
        self.register("demo", DemoProvider())
        # Aliases for local/generic providers
        self.register("ollama", OpenAICompatibleProvider(provider_name="ollama"))
        self.register("lmstudio", OpenAICompatibleProvider(provider_name="lmstudio"))
        self.register("vllm", OpenAICompatibleProvider(provider_name="vllm"))
        self.register("groq", OpenAICompatibleProvider(provider_name="groq"))
        self.register("cerebras", OpenAICompatibleProvider(provider_name="cerebras"))

    def register(self, name: str, provider: BaseProvider) -> None:
        self._providers[name.lower()] = provider

    def get(self, name: str) -> BaseProvider | None:
        return self._providers.get(name.lower())

    def available_providers(self) -> list[str]:
        return sorted(self._providers)

    def resolve(self, provider_name: str | RouteId | None) -> BaseProvider:
        """Resolve by explicit provider name only. Anything else fails closed.

        Prefix-sniffing, substring matching, and silent defaults are deleted:
        routes must declare ``provider``. Unknown or missing names raise
        ``ProviderResolutionError`` naming the available providers. A
        ``RouteId`` resolves by its provider part.
        """
        key = provider_name.provider if isinstance(provider_name, RouteId) else provider_name
        if key and key.lower() in self._providers:
            return self._providers[key.lower()]
        raise ProviderResolutionError(
            f"unknown provider {provider_name!r}; available providers: "
            f"{', '.join(self.available_providers())}"
        )
