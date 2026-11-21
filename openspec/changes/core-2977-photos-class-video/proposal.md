# Photos libraries admit camera-roll video

CORE-2977 (PHOTOS-CLASS-VIDEO-1): the plugin declares only `image`, so Core's scan treats camera-roll `.mov`/`.mp4` clips in a Photos library as media outside the plugin's declaration (`LibraryHasUndeclaredMedia`), which suspends retirement and missing-file marking for the whole library. Declare `MediaClasses` `["image","video"]`, mirroring CORE-2364 (familyarchive). The version stays 0.1.4: the SDK compatibility matrix holds producer evidence per plugin version, so the 0.1.5 bump waits for the SDK release that carries its entry.

Non-goals: EXIF for video (the exif hook stays gated to image class), duration badges and viewer routing (Core/clients). Evidence: bundle regression tests, gofmt, vet, lint, strict specs. Rollback: revert to `["image"]`.
