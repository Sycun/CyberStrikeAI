# Runtime attribution

Pi SDK: https://github.com/earendil-works/pi (MIT), pinned to 1.5.2.

The capability-selection protocol and main/child task-anchor organization in
`src/profile.ts` are adapted for CyberStrikeAI from:

- https://github.com/HunYiSage/dsh-purge — Copyright 2026 HunYiSage.
- https://github.com/lzzzz666888/dsh-purge-optimized — local reference 1.1.91-opt.2.

The source NOTICE and Apache-2.0 license are retained under `third-party/dsh-purge/`.
CyberStrikeAI uses its own prompt implementation, explicit tool registration and
host execution lifecycle. DSH binary patches, encrypted default identity prompts,
host-policy removal and credential files are not distributed with this integration.
