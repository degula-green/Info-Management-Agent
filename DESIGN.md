# Info Agent Frontend Design

## Source Of Truth

The workbench follows the Tencent WeKnora frontend at the local checkout whose remote is `https://github.com/Tencent/WeKnora.git`. The implementation keeps its TDesign Vue Next component vocabulary, TencentSans font, green brand tokens, pale sidebar, grouped command palette, setting rows, drawers, tags, and compact operate-first density. Reused source and assets remain subject to the upstream MIT license.

## Surface Rules

- The user enters through the WeKnora-like split login/register surface.
- Authenticated screens use a fixed left navigation and a top command search input.
- Search is grouped by chat, message, file, and Q&A; selecting a chat opens its complete collected context, while content opens a WeKnora-style drawer for edit/download actions.
- Knowledge bases are fixed to Feishu, Enterprise WeChat, and Personal WeChat. A source card is locked until its connector is bound; a bound source expands to chat-level knowledge bases and collection controls.
- Personal center uses WeKnora setting-row layout for editable nickname/email, visibility policy, and connector authorization.
- All prototype values are synthetic and local. No frontend fetches backend endpoints.

## Component Reuse

`src/components/GlobalCommandPalette/ResultGroup.vue` and `ResultItem.vue` are copied from WeKnora and used by `InfoCommandPalette.vue`. Theme variables, TencentSans, and the WeKnora logo are copied from the same checkout. Remaining components preserve the same TDesign markup and class grammar while replacing API/store calls with local mock state.

## Responsive Behavior

At narrow widths the sidebar collapses to icon-only navigation, page columns stack, and the command input remains the primary global action. Content controls keep their labels and move below the chat metadata instead of overflowing.
