#ifndef G1_NETCORE_H_
#define G1_NETCORE_H_

#include <stdint.h>

int g1_netcore_handshake(uint8_t role, uint8_t esb_channel, uint8_t esb_master, uint8_t esb_slave);

#endif
