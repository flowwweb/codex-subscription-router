# Compatibility

The patcher is intentionally tied to known ChatGPT desktop bundle structures.
It verifies every modified renderer, main-process, and native binary anchor and
stops instead of applying a partial patch.

## Release 0.2.0

| Component | Tested value |
| --- | --- |
| Windows platform | Windows x64 |
| Official package | `OpenAI.Codex` `26.810.7004.0` |
| Official executable product version | `151.0.7922.137` |
| Official bundle build | `6662` |
| `app.asar` SHA-256 | `c7ac6d76cf5f30aa5cb92e1e46561933c06e94e3fe2d6582a04dac18c76f3ed1` |
| Runtime backend | user-local `%LOCALAPPDATA%\\OpenAI\\Codex\\bin\\<build>\\codex.exe` |

The Windows installer verifies the selected backend hash and proves app-server
initialization before the exact versioned daemon is considered ready. A Codex
app update changes that hash and requires rerunning the installer. The official
Windows GUI still does not consume this standalone router; v0.2.0 provides the
local dashboard and an explicit app-server bridge, not GUI interception.

## Release 0.1.0

| Component | Tested value |
| --- | --- |
| Official ChatGPT version | `26.803.61601` |
| Official bundle build | `6396` |
| `app.asar` SHA-256 | `d5a44ed9e2f1db5f81dbbe85408aed256f3203c5b16f00817bb9d7cd941343cf` |
| Architecture | Apple silicon (`arm64`) |

A different official version may work when all anchors remain identical, but
it is unverified. The patcher rejects a version, build, or ASAR hash mismatch by
default; `--allow-untested-source` is an explicit diagnostic override. Never
weaken an anchor-count or binary-constant check merely to make a new build
complete. Review the upstream change and update the patch deliberately.

## Original Windows adapter observation

The Windows adapter was exercised against the locally installed package with:

| Component | Observed value |
| --- | --- |
| Platform | Windows x64 |
| Official package | `OpenAI.Codex` |
| Official app package version | `26.810.7004.0` |
| Official bundle build | `6662` |
| `app.asar` SHA-256 | `c7ac6d76cf5f30aa5cb92e1e46561933c06e94e3fe2d6582a04dac18c76f3ed1` |
| Official package CLI asset | `resources\\codex.exe` (integrity reference) |
| Runtime backend | `%LOCALAPPDATA%\\OpenAI\\Codex\\bin\\e305f1c75d8da435\\codex.exe` |

This records the package and runtime backend used for standalone adapter proof;
the Windows Store package's bundled CLI asset is protected by package
execution rules, so the adapter uses the user-local backend. The current GUI
build does not consume the router override for its local app-server. It is not
a claim that the macOS patcher or Windows renderer anchors support this build.
