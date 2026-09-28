// One TCP connection ("lane") of the PCM stream with its receive buffer.
// Kept in a header: the Arduino builder puts function prototypes before any struct in the .ino.
#pragma once
#include <stdint.h>
#include <stddef.h>

struct Lane {
  int fd = -1;
  uint8_t buf[8192];
  size_t pos = 0, len = 0;
};
