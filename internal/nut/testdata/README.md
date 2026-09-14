# testdata

Verbatim captures of real `upsd` replies, taken with:

```bash
printf 'LIST VAR <name>\nLOGOUT\n' | nc <host> 3493 > listvar_<device>.txt
```

Tests parse these rather than hand-written lines, so the client is exercised
against bytes the devices actually emit — different NUT versions, different
drivers, different quirks.

Hardware serial numbers have been replaced with placeholders of identical
length and padding. The padding is deliberate: one device pads fixed-width
fields out of its own buffers, which is what `VarsToMap` trims.

Add a capture when a new device joins, and a case to `fixtures` in
`protocol_test.go`.
