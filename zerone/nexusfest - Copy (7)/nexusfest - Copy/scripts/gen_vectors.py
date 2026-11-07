import os
import struct
import zlib

os.makedirs("internal/codec/testdata", exist_ok=True)

def crc32_ieee(data: bytes) -> int:
    return zlib.crc32(data) & 0xFFFFFFFF

# 1. Active slot (256 bytes)
active = bytearray(256)
struct.pack_into("<H", active, 0, 0x5352)  # Magic
active[2] = 1                              # RecordFormat
active[3] = 1                              # StatusActive
struct.pack_into("<I", active, 4, 20240001) # StudentID
struct.pack_into("<Q", active, 8, 42)       # RecordVersion
active[16:20] = b"Asha"
active[48:51] = b"Rao"
struct.pack_into("<i", active, 80, 12555)  # DateOfBirth days (2004-05-17)
active[84] = 1                             # female
active[88:108] = b"asha.rao@example.com"
active[152:166] = b"+91-9000000000"
active[172:188] = b"Computer Science"
struct.pack_into("<H", active, 196, 2022)  # Year
struct.pack_into("<H", active, 198, 375)   # GPAx100
struct.pack_into("<Q", active, 200, 1715904000) # CreatedAt
struct.pack_into("<Q", active, 208, 1715904000) # UpdatedAt
c = crc32_ieee(active[:252])
struct.pack_into("<I", active, 252, c)
with open("internal/codec/testdata/active_slot.hex", "w") as f:
    f.write(active.hex())

# 2. Free slot (256 zeros)
free = bytes(256)
with open("internal/codec/testdata/free_slot.hex", "w") as f:
    f.write(free.hex())

# 3. Deleted slot (tombstone)
deleted = bytearray(256)
struct.pack_into("<H", deleted, 0, 0x5352)
deleted[2] = 1
deleted[3] = 2  # StatusDeleted
struct.pack_into("<I", deleted, 4, 20240001)
struct.pack_into("<Q", deleted, 8, 42)
c_del = crc32_ieee(deleted[:252])
struct.pack_into("<I", deleted, 252, c_del)
with open("internal/codec/testdata/deleted_slot.hex", "w") as f:
    f.write(deleted.hex())

# 4. Corrupt slot (bad CRC)
corrupt = bytearray(active)
corrupt[252] ^= 0xFF
with open("internal/codec/testdata/corrupt_slot.hex", "w") as f:
    f.write(corrupt.hex())

print("Golden vectors generated successfully in internal/codec/testdata/")

