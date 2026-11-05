# Remote Buff equipment registration admission

The native registration consumer clears the previous actor registration before
reading its rows. It then tests the receiving player's local inventory-busy flag
for every row, including rows describing another player. When that flag is set,
remote rows are consumed from the packet but never applied to the remote Buff
manager. No registration, equipment or skill change by the caster is required.

The fixed-input builder limits that guard to the local actor. Remote rows retain
the original packet reads, item construction, actor key, registration manager,
and cleanup. Local rows retain the original busy policy. It uses 31 bytes of
verified zero padding in the existing RX section, updates its virtual extent and
PE checksum, and rejects every byte change outside the explicit ranges. File
size and section count stay unchanged. It never installs or starts the client.

Supported complete SHA256 inputs, both 112607232 bytes:

- `e142a7e6d530a0e369c722cb7fcc90c18f2d18884a343096d88336aed29f75e4`
- `fbb0b5d6c12387242745c15ea947e4cf2651aa2d80ce99d9e3256b18c3e8100d`
  (the same cumulative input with the existing solo Anton window recipe)

Use new output files:

```text
python build_candidate.py --input archived.exe --output candidate.exe
python verify_candidate.py --input archived.exe --candidate candidate.exe --output replay.json
```

The verifier executes the complete native consumer, inventory selector, busy
getter, local-actor getter and helper in x86 emulation for 24 cases, comparing
original and candidate behavior. Packet readers, item factory/vtable leaves,
registration manager writes, allocation and UI/debug leaves are explicit
fixtures. It checks full packet consumption, preserved registers/stack and exact
actor/slot/item arguments, including empty and repeated equipment rows. This is
not renderer or effective Buff-stat validation. The reported incident did not
record the receiver's busy flag, so this deterministic admission defect is not
claimed as the proven cause of that incident. Production release and gameplay
acceptance remain separate.
