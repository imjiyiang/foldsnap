# Foldsnap visual assets

- `app-icon.png`: original macOS icon master with transparent outer margin.

Use this single master throughout the app, website and menu bar. The macOS `.icns` and full-color menu-bar image are generated from it by `packaging/macos/package.sh` using the system `sips` and `iconutil` tools. Web favicon/touch variants are in `apps/web/public` and are resized from the same master.

The PNG was generated with the built-in OpenAI image generation tool from an original prompt and reviewed for this project; no third-party reference image was used. These project assets are distributed under the repository's MIT license. The image generation prompt is saved in `prompt.txt` for provenance; subsequent generations may differ.
