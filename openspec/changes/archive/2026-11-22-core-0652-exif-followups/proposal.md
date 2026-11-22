# EXIF reader follow-ups and camera-roll video capture date

CORE-0652 (plugin-data items) and CORE-3023. `peekSignature` returns a non-EOF read error instead of dropping it; RAW container reads observe context cancellation between chunks; `isoBoxes` is replaced by `isoFind`, which stops at the first match; the RAF and CR3 fuzz seeds assert a decoded value. The local-metadata hook now accepts class `video` (Core derives the class from the probe) and gives a clip its mtime capture date and folder album, since the reader decodes no video EXIF.

Skipped: real-camera RAW fixtures, the Depends link, and rescan location idempotence (Core upserts entities by library, kind and title).
