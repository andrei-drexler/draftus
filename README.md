[Discord](https://discordapp.com/) bot designed to help with draft cup organization (more specifically, with player registration and picking).
In order to keep the channels it is active in as spam-free as possible, the bot expects to have the *Manage messages* permission.
See it in action by joining [OverFrag's discord server](http://overfrag.com/index.php/discord/).

# Supported commands

(Note: arguments marked `[]` are *optional*, `<>` are **mandatory**)

Type... | In order to...
:--- | :---
?draft help              |Show this list
?draft start `[message]`   |Start a new cup, with an optional description
?draft abort             |Abort current cup
?draft add               |Sign up to play in the cup
?draft remove `[number]`  |Remove yourself from the cup (or another player, if admin)
?draft who               |Show list of players in cup
?draft moderate `[on\|off]` |Enable/disable or toggle channel moderation when a cup is active
?draft close `[number]`    |Close cup for sign-ups, optionally keeping only [number] players
?draft pick `<number>`     |Pick the player with the given number
?draft promote           |Promote the cup
?draft reopen            |Discard current teams and reopen cup for sign-up
