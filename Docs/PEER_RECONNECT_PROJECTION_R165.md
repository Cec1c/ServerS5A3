# R165 peer reconnect projection

User evidence: the leader cannot see the member model but still sees skills;
the member's view is normal. A preceding report described test78 reconnecting
after First Hatchery and losing the peer model and Black Volcano damage.

Read-only current database identity lookup maps test78 to 1006, test99 to 1047,
and 王 to 2046. Application logs show test78 binding a replacement Anton-channel
connection at 19:21:44 HKT while 王 retains its earlier connection. Raw packet
logging is disabled, so these logs cannot prove which model or barrier packet
was delivered, nor establish the native crash cause.

The old projection cache stores only character IDs and the finish-loading
resend cache stores only a room/party marker. Replacing a peer connection while
retaining the receiver and run therefore suppresses both projection paths.
The regression retains the receiver, replaces the peer through bind/promote,
then inspects actual emitted mode0/mode1/op9 frames. The candidate keys model
projection by the bound peer identity and keys resend by the full run and that
identity. Retired connections cannot initiate another projection. Failure
releases only the claim belonging to the failed identity. Old resend identities
are retired to avoid retaining every disconnected socket in a long raid room.

Targeted validation before the final diagnostic additions: 11 leaves pass,
0 fail/skip, including same-run join, cross-run/map isolation and retry. The
initial negative run reproduced zero replacement actor output at the retained
leader. Final cumulative validation is recorded outside the repository.

The new application-log allowlist keeps model delivery, landing decisions and
existing barrier symbol evidence available without enabling raw packet logs.
These are server write results, not native rendering or damage confirmation.

Still open: physical acceptance of reconnect/regroup, First Hatchery crash,
Black Volcano barrier/damage, D03 new FiendWar party formation inside an already
entered dungeon, and pure-white FiendWar movement. User separately confirmed
dark FiendWar movement works. None is closed by the projection tests.
