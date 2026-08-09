package io.tombstone.types;

import org.junit.jupiter.api.Test;
import java.util.List;
import static org.junit.jupiter.api.Assertions.*;

public class FlagEnvironmentStateTest {
    @Test void testConstructWithAllFields() {
        var prereq = new FlagPrerequisite("base-flag", "true", true);
        var condition = new PropertyCondition("plan", "eq", List.of("pro"), false);
        var rule = new TargetingRule("r1", List.of(condition), 100.0, "matched", 0);

        var state = new FlagEnvironmentState(
            "id-1", "test-flag", "test", true, 50, "false", 0L,
            List.of(prereq), List.of(rule), List.of("user-1"), 2
        );

        assertEquals(1, state.prerequisites().size());
        assertEquals("base-flag", state.prerequisites().get(0).flagKey());
        assertEquals(1, state.targetingRules().size());
        assertEquals("plan", state.targetingRules().get(0).conditions().get(0).attribute());
        assertEquals(List.of("user-1"), state.targetList());
        assertEquals(2, state.hashVersion());
    }

    @Test void testDefaultHashVersionIsOneViaConvenienceFactory() {
        var state = FlagEnvironmentState.simple("id-1", "test-flag", "test", true, 50, "false", 0L);
        assertEquals(1, state.hashVersion());
        assertTrue(state.prerequisites().isEmpty());
        assertTrue(state.targetingRules().isEmpty());
        assertTrue(state.targetList().isEmpty());
    }
}
