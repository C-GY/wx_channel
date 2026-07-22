const fs = require('fs');
const path = require('path');

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

const homeSource = fs.readFileSync(path.resolve(__dirname, 'home.js'), 'utf8');
const feedSource = fs.readFileSync(path.resolve(__dirname, 'feed.js'), 'utf8');

assert(homeSource.includes("button.id = 'wx-home-creative-radar-sync'"), 'home video toolbar should render the Creative Radar button');
assert(homeSource.includes('__handle_home_creative_radar_sync_click(button)'), 'home Creative Radar button should have a click handler');
assert(homeSource.includes('__sync_single_video_to_creative_radar__(profile, actionButton)'), 'home click should use the shared single-video queue flow');
assert(homeSource.includes('__position_home_creative_radar_button(existingRadarButton, existing)'), 'home button should stay positioned beside the download icon');
assert(homeSource.includes('__reset_home_creative_radar_button(__get_active_home_feed_id())'), 'home scrolling should reset the button for the newly active video');

assert(feedSource.includes("button.id = 'wx-feed-creative-radar-sync'"), 'feed video toolbar should render the Creative Radar button');
assert(feedSource.includes('__handle_feed_creative_radar_sync_click(button)'), 'feed Creative Radar button should have a click handler');
assert(feedSource.includes('__sync_single_video_to_creative_radar__(profile, actionButton)'), 'feed click should use the shared single-video queue flow');
assert(feedSource.includes('container.insertBefore(radarSyncButton, container.firstChild)'), 'feed button should be inserted beside the download icon');
assert(feedSource.includes('__reset_feed_creative_radar_button(activeFeedId)'), 'feed scrolling should reset the button for the newly active video');

console.log('single_video_sync_ui.test.js passed');
