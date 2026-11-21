## ADDED Requirements

### Requirement: Photos declares image and video media classes

The bundle SHALL declare both the `image` and `video` Media classes, so camera-roll clips beside photos are admitted as video and existing images stay image.

#### Scenario: A camera-roll clip beside a photo

- **WHEN** a Photos library holds a `.jpg` and a `.mov` or `.mp4` clip
- **THEN** both are within the plugin's declared classes: the photo as `image`, the clip as `video`
