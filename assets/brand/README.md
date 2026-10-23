# Foldsnap visual assets

- `app-icon.png`: original macOS icon master with transparent outer margin.
- `mark.svg`: editable compact single-color companion for the web and menu bar.
- `status-icon.png`: 64 px transparent template rendition of the SVG, generated with `rsvg-convert`.

The macOS `.icns` is generated from the master by `packaging/macos/package.sh` using the system `sips` and `iconutil` tools. Web favicon/touch variants are in `apps/web/public`. Do not rescale the menu-bar mark into the full-color app icon.

The PNG was generated with the built-in OpenAI image generation tool from an original prompt and reviewed for this project; no third-party reference image was used. The SVG companion was drawn as editable source. These project assets are distributed under the repository's MIT license. The image generation prompt is saved in `prompt.txt` for provenance; subsequent generations may differ.
