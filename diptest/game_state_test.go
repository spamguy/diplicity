package diptest

import "testing"

func testGameState(t *testing.T) {
	g0 := startedGames[0]
	g1 := startedGames[1]
	nat0 := startedGameNats[0]
	nat1 := startedGameNats[1]

	startedGameEnvs[0].
		GetRoute("GameState.Load").
		RouteParams("game_id", startedGameID, "nation", nat0).Success().
		AssertNil("Properties", "Muted")

	g0.Follow("game-states", "Links").Success().AssertLen(7, "Properties").
		Find(nat0, []string{"Properties"}, []string{"Properties", "Nation"}).
		Follow("update", "Links").Body(map[string]interface{}{
		"Muted": []string{nat1},
	}).Success()

	startedGameEnvs[0].
		GetRoute("GameState.Load").
		RouteParams("game_id", startedGameID, "nation", nat0).Success().
		AssertEq([]interface{}{nat1}, "Properties", "Muted")

	g0.Follow("game-states", "Links").Success().AssertLen(7, "Properties").
		Find(nat0, []string{"Properties"}, []string{"Properties", "Nation"}).
		AssertEq([]interface{}{nat1}, "Properties", "Muted")

	g0.Follow("game-states", "Links").Success().AssertLen(7, "Properties").
		Find(nat1, []string{"Properties"}, []string{"Properties", "Nation"}).
		AssertNil("Properties", "Muted").AssertNil("Links")

	g1.Follow("game-states", "Links").Success().AssertLen(7, "Properties").
		Find(nat0, []string{"Properties"}, []string{"Properties", "Nation"}).
		AssertEq([]interface{}{nat1}, "Properties", "Muted")

	g1.Follow("game-states", "Links").Success().AssertLen(7, "Properties").
		Find(nat1, []string{"Properties"}, []string{"Properties", "Nation"}).
		AssertNil("Properties", "Muted")

}
