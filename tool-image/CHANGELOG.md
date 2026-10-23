# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Add `view_image` for workspace-relative and absolute local image paths.
- Validate PNG, JPEG, WebP and GIF content and return immutable image assets;
  extract GIF first frames as PNG images.
- Bound file/output size, decoded dimensions and concurrent processing, and
  preserve cancellation and asset-store errors.
