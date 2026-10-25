# Default skill commands and inspected character refresh

The builder accepts only the reviewed v170 image with SHA256
`e5751354901fc142324d11cfc717e4a958396877f1c0fc545f51645f8e77af55`.
It composes the title-request marker extension with three narrow repairs:

- An absent custom skill command serializes the skill's own PVF default before
  registration. Explicit saved commands retain their existing path.
- A mode8 detail refresh updates the active actor only when the open personal
  information window already binds that exact actor and namespace. Background
  roster priming keeps its auxiliary actor and quiet UI path.
- The personal information panel falls back to the initialized auxiliary actor
  only when the ordinary actor directory has no matching object. This supplies
  weapon and learned mastery calculations when inspecting across scenes.

The server must send complete learned skills and current equipment in mode3,
and track the latest successful inspection for unrelated same-area viewers.
The server clears that relationship when either connection changes character
or disconnects, and rechecks identity, ownership and visibility before sending.

`build_candidate.py --baseline INPUT --output NEW_DIRECTORY` produces the image
and exact byte-change receipt. `verify_candidate.py --baseline INPUT
--candidate OUTPUT_EXE --output NEW_JSON` reconstructs the complete image,
checks Windows read-only image mapping, and executes the emitted helpers with
bounded native serializer, window-reader and ABI cases. Unicorn and Windows
are needed for verification; the builder uses the standard library.

All work is offline. Actor lookup, UI construction and allocator leaves in the
native checks use explicit fixtures. These checks do not establish end-to-end
gameplay acceptance, which remains a separate user test.
