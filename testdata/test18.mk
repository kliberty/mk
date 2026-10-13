# Recipe variables inherited from the environment (as when mk is run from
# another mk's recipe) must not leak into this mkfile's recipes.
bye-%:V:
	echo Bye $stem $target

plain:V:
	echo plain $stem
