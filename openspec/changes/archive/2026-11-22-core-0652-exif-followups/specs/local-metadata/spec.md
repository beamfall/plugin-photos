## ADDED Requirements

### Requirement: The local-metadata hook accepts image and video Media
The Photos local-metadata hook SHALL accept Media of class `image` or `video` and SHALL refuse any other class with `ErrNotImageClass`. A `video` file SHALL get its capture date from the file's modification time, with `CaptureTimeFromMtime` set, and its folder album, because the reader decodes no video EXIF.

#### Scenario: A camera-roll clip is dated from its mtime
- **WHEN** the hook extracts a `video` file whose folder is an album
- **THEN** it returns the file's modification time as the capture date, flags `CaptureTimeFromMtime`, and names the folder album

#### Scenario: Other classes are refused
- **WHEN** the hook is called with class `audio`
- **THEN** it returns an error wrapping `ErrNotImageClass`
