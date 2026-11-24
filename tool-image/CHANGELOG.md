# Changelog

All notable changes to this plugin are documented in this file.

## 0.1.0 - 2026-11-24

### Build

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

### Added

- Add `view_image` for workspace-relative and absolute local image paths.
- Validate PNG, JPEG, WebP and GIF content and return immutable image assets;
  extract GIF first frames as PNG images.
- Bound file/output size, decoded dimensions and concurrent processing, and
  preserve cancellation and asset-store errors.
