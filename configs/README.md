Notes
===

Capturing IR signals from a Bryant IR-based remote (for a ductless system) to be
able to replicate and playback the messages via software (allowing for home/lan
integrations). _This method would work for other ductless systems from other manufacturers
though some changes may be required_. Other USB transceivers may also work but that
would likely require various changes to get lirc working properly.

## Context

The reason all of this is even necessary is that `lirc` by itself has a number of tools all around dealing
with IR reception/transmission BUT `irw`, `irrecord`, and `irsend` only understand the "remotes" (or codes) that the
underlying `lirc` configs understand. Most of these are for things like TV remotes (e.g. power on, volume up) and not for
anything quite as specific as a ductless AC remote. In order to _talk_ to such a different device one has to first get the
raw data from the remote in question and then "teach" lirc how to speak those codes (via configuration).

## Hardware

- Alpine Linux (3.13), lirc 0.10.1-r0
- x86-64 server (also testing on a pi4 with Alpine 3.13)
- [Irdroid USB IR Transceiver](https://www.irdroid.com/irdroid-usb-ir-transceiver/)
- Bryant ductless system (Models: 619PAQXXXBBMA, 619PEQXXXBBMA)

## Setup

- USB transceiver plugged in
- lirc installed
- `lsusb` reports the Irdroid device

## Capturing

We need to capture the _raw_ device inputs, to do that:
```
mode2 -d /dev/ttyACM0 -H irtoy > output
```

_Press a **single** button on the remote and then CTRL+C_

There is an extraneous spacing output that will probably be the final output line, this is not part of the code we need so remove it
```
sed -i '$ d' output
```

Now make sure we only capture the code outputs (and none of the other extra `mode2` outputs)

```
cat output | grep '^(spac|pulse)' | cut -d " " -f 2 | tr '\n' ' ' > code
```

This will have captured a single button press. It's important to understand that
these remotes maintain the _state_ of the system and therefore a "power on" press
at 72 degrees will show up differently than a "power on" at 74 degrees (basically
many commands must be captured if a lot of settings are wanted)

## Config

First define the remote type and that we're using raw codes to communicate

```
vim bryant.conf
---
# ACSTOP (74 degrees)
# ACSTART (74 degrees)
begin remote

    name BRYANT
    flags RAW_CODES
    eps 30
    aeps 100

    ptrail 0
    repeat 0 0
    gap 40991

    begin raw_codes
```

Next specify the name of the command to include
```
vim bryant.conf
---
        name ACSTOP
```

Finally the raw output
```
cat code >> bryant.conf
```

_For any additional codes one must just create a `name <NAME>` and then the output (see the full example below)_

Close-out the remote configuration
```
    end raw_codes
end remote
```

## lirc

Now make sure to run `lircd` with the configuration

```
ircd -H irtoy -d /dev/ttyACM0 bryant.conf
```

and then send commands!

```
irsend SEND_ONCE BRYANT ACSTART
```

## Example

```
# ACSTOP (74 degrees)
# ACSTART (74 degrees)
begin remote

    name BRYANT
    flags RAW_CODES
    eps 30
    aeps 100

    ptrail 0
    repeat 0 0
    gap 40991

    begin raw_codes
        name ACSTOP
            4415 4394 554 1578 554 511 554 1578 554 511 554 511 554 511 554 511 554 1578 554 511 554 511 554 1578 554 511 554 511 554 511 554 511 554 511 554 511 554 1578 554 1578 554 511 554 1578 554 1578 554 511 554 511 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 511 554 511 554 511 554 511 554 1578 554 511 554 5183 4415 4394 554 511 554 1578 554 511 554 1578 554 1578 554 1578 554 1578 554 511 554 1578 554 1578 554 511 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 511 554 511 554 1578 554 511 554 511 554 1578 554 1578 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 1578 554 1578 554 1578 554 1578 554 511 554 1578 554
        name ACSTART
            4415 4394 554 1578 554 511 554 1578 554 511 554 511 554 511 554 511 554 1578 554 1578 554 511 554 1578 554 511 554 511 554 511 554 511 554 511 554 511 554 1578 554 1578 554 511 554 1578 554 1578 554 511 554 511 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 511 554 1578 554 511 554 511 554 511 554 511 554 1578 554 511 554 5183 4415 4394 554 511 554 1578 554 511 554 1578 554 1578 554 1578 554 1578 554 511 554 511 554 1578 554 511 554 1578 554 1578 554 1578 554 1578 554 1578 554 1578 554 511 554 511 554 1578 554 511 554 511 554 1578 554 1578 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 511 554 1578 554 511 554 1578 554 1578 554 1578 554 1578 554 511 554 1578 554
    end raw_codes
end remote
```

