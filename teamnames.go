package main

////////////////////////////////////////////////////////////////
// Random team name support
////////////////////////////////////////////////////////////////

func decomposeName(index int) (int, int) {
	attribute := index % len(Attributes)
	noun := index / len(Attributes)
	return attribute, noun
}

// Random team names
var (
	Attributes = [...]string{
		"Black", "Grey", "Purple", "Brown", "Blue", "Red", "Green", "Magenta",
		"Silent", "Quiet", "Loud", "Thundering", "Screaming", "Flaming", "Furious", "Zen", "Chill",
		"Jolly", "Giggly", "Unimpressed", "Serious",
		"Inappropriate", "Indecent", "Sexy", "Hot", "Flirty", "Cheeky", "Cheesy", "Shameless", "Provocative", "Offensive", "Defensive",
		"Gangster", "Fugitive", "Outlaw", "Pirate", "Thug", "Kleptomaniac", "Killer", "Lethal", "Gunslinging",
		"Fresh", "Rookie", "Trained", "Major", "Grandmaster", "Retired", "Potent", "Mighty", "Convincing", "Commanding", "Punchy",
		"Lucky", "Tryhard", "Stronk",
		"Expendable",
		"Millenial", "Centennial",
		"Lunar", "Solar", "Martian",
		"Aerodynamic",
		"Sprinting", "Strafing", "Strafejumping", "Circlejumping", "Bunnyhopping", "Crouching", "Rising", "Standing", "Camping", "Twitchy", "Sniping", "Telefragging",
		"Rolling", "Dancing", "Breakdancing", "Tapdancing", "Clubbing",
		"Snorkeling", "Snowbording", "Cycling", "Rollerblading", "Paragliding", "Skydiving",
		"Drifting", "Warping", "Laggy", "Smooth", "Stiff",
		"Cryogenic", "Mutating", "Undead", "Ghostly", "Possessed", "Supernatural",
		"Juggling", "Ambidextrous", "Left-handed",
		"Snoring", "Sleepy", "Energetic", "Hyperactive", "Dynamic",
		"Tilted", "Excentric", "Irrational", "Claustrophobic",
		"Undercover", "Stealthy", "Hidden", "Obvious", "Deceptive",
		"Total", "Definitive",
		"Chocolate", "Vanilla",
		"Plastic", "Metal", "Rubber", "Golden", "Silver", "Paper",
		"Random", "Synchronized", "Synergetic", "Coordinated",
		"Radical", "Unconventional", "Standard", "Original", "Mutated", "Creative", "Articulate", "Elegant", "Gentle", "Polite", "Classy",
		"Retro", "Old-school", "Next-gen", "Revolutionary",
		"Punk", "Disco", "Electronic", "Analog", "Mechanical",
		"Wireless", "Aircooled", "Watercooled", "Overvolted", "Overclocked", "Idle", "Hyperthreaded", "Freesync", "G-Sync", "Crossfire", "SLI", "Quad-channel",
		"Optimized", "Registered", "Licensed",
		"Nerdy", "Hipster", "Trendy", "Sporty", "Chic", "Photogenic",
		"Mythical", "Famous", "Incognito",
		"Slim", "Toned", "Muscular", "Round", "Heavy", "Well-fed", "Hungry", "Vegan",
		"Bearded", "Hairy", "Furry", "Fuzzy",
		"Beastly", "Barbarian", "Vicious", "Fierce", "Devastating", "Dominating", "Conquering", "Controlling", "Agressive", "Retaliating",
		"Fearless", "Heroic", "Glorious", "Victorious", "Triumphant", "Relentless", "Unstoppable", "Spectacular", "Impressive", "Rampage",
		"Arctic", "Polar", "Siberian", "Tropical", "Brazilian",
	}

	Nouns = [...]string{
		"Alligators", "Crocs",
		"Armadillos", "Beavers", "Squirrels", "Raccoons",
		"Bears", "Pandas",
		"Hamsters", "Kittens", "Bunnies", "Puppies", "Pitbulls", "Bulldogs", "Dalmatians", "Greyhounds", "Huskies",
		"Turtles",
		"Giraffes", "Gazelles",
		"Sharks", "Piranhas", "Tuna", "Salmons", "Trouts", "Barracudas", "Stingrays",
		"Dolphins", "Sealions",
		"Hornets",
		"Pythons", "Vipers", "Cobras", "Anacondas",
		"Hippos", "Rhinos",
		"Tigers", "Cheetas", "Hyenas", "Dingos",
		"Baboons", "Bonobos",
		"Dragons", "Pterodactyls", "Eagles", "Hawks", "Ravens", "Seagulls", "Flamingos", "Pigeons", "Roosters", "Duckies",
		"Ponies", "Zebras", "Stallions",
		"Zombies", "Unicorns", "Mermaids", "Trolls",
	}

	TeamNameCombos = len(Attributes) * len(Nouns)
)
