# R195 current-client receive admission

The fixed input is the deployed v153 client (`c7ba1bfe81d7283c97e33c6a3449f729b63e79a916357aa7ea69fb7754709bc8`, 111857664 bytes). Its receive call at VA `0x2523A13` calls the existing gate at `0x6EAA000`.

Executing that call confirms that registered receivers are unreachable for these server-produced pairs:

| Classification | Opcode | Consumer |
| --- | --- | --- |
| 0 | 103 | Absolute creature satiety update |
| 0 | 775 | Absolute Luke phase coin count |
| 0 | 1278 | Aiken set record |
| 0 | 1367 | Buff equipment rows without skill initialization |
| 0 | 1368 | Full buff registration initialization |
| 1 | 835 | Guild gem synthesis result |
| 1 | 864 | Guild medal synthesis result |

`patch_receivers.py` appends an executable `.r195rx` section and redirects the single receive call to it. These exact pairs return accepted. Every other pair tail-calls the old gate. It validates the fixed baseline, PE layout and byte-difference scope before writing the candidate. There is no permission for opcode 555, 1426 or 1369.

`verify_receivers.py` independently parses both PE files without importing the producer. It verifies sections, checksum, byte-difference scope and invokes the real receive call and gate chain for all 65536 opcodes in each classification 0 and 1. Exactly the seven pairs above may differ. It also checks classification 2/255 controls, the three existing job-change admissions, stack balance and preserved registers.

```text
python patch_receivers.py --help
python verify_receivers.py --help
```

Use the project's external serial memory guard for builds and native checks. The verifier requires the external Unicorn dependency directory. No client executable or dependencies belong in the repository.

The earlier six-pair candidate (`753f0a12d597595bb078f7a5ee708ec0d547e8275ca0fa7019423cd07aec1bad`) is discarded: its 103 admission used the wrong classification. The registry/dispatcher execution in `resume/notification-registry.json` proves 103 and 775 are class0 notifications. Both have no ACK status prefix. Direct receiver execution alone did not prove classification. Preserve old receipts as historical evidence; do not deploy that candidate or its table2 derivative. The seven-pair candidate requires fresh build and independent verification receipts.

The corrected candidate is `8e92c29cbbbcc2e643a87cb73e199ec52e9b3beeb4146b3ec16d62404d36064c` (111861760 bytes; 118-byte helper). Fresh `receivers7-build.json` and `receivers7-verify.json` passed 131072 native cases per image and 18 additional classification controls, with exactly the seven changes above.

The op103 receiver and gauge setter passed 12 native cases. Separately, actual server-exported op44/op103 bytes executed the v153 receivers, confirming item-use/debit/refresh order and absolute gauge 35; packet readers, lookup, virtual item-use and UI leaves are fixtures. This proves admission, calling convention and the tested receiver behavior. It does not prove full packet consumers, graphical UI behavior, the item subclass skill-charge effect, live timing or online acceptance.

The server's committed-equipment path now uses opcode 1367 only after the same character generation and skill have a successful full snapshot. Row updates must still run for a rebuilt visible actor even when inventory coordinates are identical. The server and this candidate are a compatibility unit and must be validated together before deployment.
