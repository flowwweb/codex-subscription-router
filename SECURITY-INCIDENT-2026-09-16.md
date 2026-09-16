# Security incident containment: 16 September 2026

The identified hidden VS Code folder-open Node launcher and its font-named payload are removed. Automatic tasks are disabled and real environment-file exclusions restored.

Existing clones, historical commits, tags, releases and cached packages are NOT cleared. Do not run affected old checkouts or assume pulling this commit repairs a machine. Preserve evidence, isolate potentially affected machines/runners and assess persistence. Rotate credentials accessible to an affected environment from a known-clean device and invalidate relevant sessions. Never put replacement credentials on an unassessed machine.

This is targeted source containment. Payload execution, credential theft, entry point and attribution have not been established. Endpoint recovery, credential rotation, workflow/deployment review and published distribution clearance remain separate required steps. Keep builds and releases paused pending review. No history rewrite or force-push is performed.
