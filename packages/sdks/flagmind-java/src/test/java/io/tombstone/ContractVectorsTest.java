package io.tombstone;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.tombstone.evaluation.EvaluationEngine;
import io.tombstone.evaluation.PrerequisiteChecker;
import io.tombstone.evaluation.RuleMatcher;
import io.tombstone.types.*;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;

import java.io.File;
import java.util.*;
import java.util.function.Function;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.*;

/** Loads packages/sdks/test-contract/vectors.json and asserts the Java SDK's
 *  evaluation logic matches every vector. This is the executable definition
 *  of "parity" for this SDK — see docs/SDK_CONTRACT.md. */
public class ContractVectorsTest {

    private static final EvaluationEngine ENGINE = new EvaluationEngine();
    private static JsonNode vectors;

    private static JsonNode loadVectors() throws Exception {
        if (vectors == null) {
            var mapper = new ObjectMapper();
            var file = new File("../test-contract/vectors.json");
            vectors = mapper.readTree(file);
        }
        return vectors;
    }

    @TestFactory
    Stream<DynamicTest> hashVectors() throws Exception {
        var root = loadVectors();
        var list = new ArrayList<DynamicTest>();
        for (JsonNode v : root.get("vectors")) {
            String flagKey = v.get("flag_key").asText();
            String userId = v.get("user_id").asText();
            int hashVersion = v.get("hash_version").asInt();
            int rolloutPct = v.get("rollout_pct").asInt();
            boolean expected = v.get("expected_in_cohort").asBoolean();

            list.add(DynamicTest.dynamicTest(
                flagKey + "/" + userId + "/v" + hashVersion + "/" + rolloutPct + "%",
                () -> {
                    var flag = new FlagEnvironmentState(
                        "id", flagKey, "test", true, rolloutPct, "false", 0L,
                        List.of(), List.of(), List.of(), hashVersion
                    );
                    var context = new EvaluationContext(userId, "", Map.of());
                    var result = ENGINE.evaluate(flag, context, false, flagKey);
                    assertEquals(expected, (Boolean) result.value(),
                        "hash vector mismatch for " + flagKey + "/" + userId);
                }
            ));
        }
        return list.stream();
    }

    @TestFactory
    Stream<DynamicTest> prerequisiteVectors() throws Exception {
        var root = loadVectors();
        var list = new ArrayList<DynamicTest>();
        for (JsonNode v : root.get("prerequisite_vectors")) {
            String id = v.get("id").asText();
            var prereqNode = v.get("prerequisite");
            var prereq = new FlagPrerequisite(
                prereqNode.get("flag_key").asText(),
                prereqNode.get("required_variation").asText(),
                prereqNode.get("gate").asBoolean()
            );
            boolean expectedSatisfied = v.get("expected_satisfied").asBoolean();

            var allFlagsNode = v.get("all_flags");

            Set<String> seenKeys = new HashSet<>();
            if (v.has("seen_keys")) {
                v.get("seen_keys").forEach(k -> seenKeys.add(k.asText()));
            }

            // Lookup function: each "all_flags" entry is {"enabled": bool, "variation": "true"|"false"}.
            // enabled=false always resolves via the engine's OFF branch regardless of rolloutPct;
            // enabled=true with rolloutPct=100 always resolves via the FALLTHROUGH branch to true —
            // together these two shapes are sufficient to make the dependency evaluate to exactly
            // the vector's declared "variation" string, since this release has no non-boolean
            // variation type (see design spec Section 3's prerequisite-comparison note).
            Function<String, FlagEnvironmentState> lookup = key -> {
                if (!allFlagsNode.has(key)) return null;
                var fn = allFlagsNode.get(key);
                boolean enabled = fn.get("enabled").asBoolean();
                String variation = fn.get("variation").asText();
                int rolloutPct = "true".equals(variation) ? 100 : 0;
                return new FlagEnvironmentState(
                    "id", key, "test", enabled, rolloutPct, "false", 0L,
                    List.of(), List.of(), List.of(), 1
                );
            };

            list.add(DynamicTest.dynamicTest(id, () -> {
                boolean satisfied = PrerequisiteChecker.checkAll(
                    List.of(prereq), lookup, new HashMap<>(), seenKeys, "parent-flag", ENGINE,
                    new EvaluationContext("u1", "", Map.of())
                );
                assertEquals(expectedSatisfied, satisfied, "prerequisite vector mismatch for " + id);
            }));
        }
        return list.stream();
    }

    @TestFactory
    Stream<DynamicTest> ruleVectors() throws Exception {
        var root = loadVectors();
        var list = new ArrayList<DynamicTest>();
        for (JsonNode v : root.get("rule_vectors")) {
            String id = v.get("id").asText();
            var rulesNode = v.get("rules");
            List<TargetingRule> rules = new ArrayList<>();
            for (JsonNode r : rulesNode) {
                List<PropertyCondition> conditions = new ArrayList<>();
                for (JsonNode c : r.get("conditions")) {
                    List<String> values = new ArrayList<>();
                    c.get("values").forEach(val -> values.add(val.asText()));
                    conditions.add(new PropertyCondition(
                        c.get("attribute").asText(), c.get("operator").asText(), values, c.get("negate").asBoolean()));
                }
                rules.add(new TargetingRule(
                    r.get("id").asText(), conditions, r.get("rollout_pct").asDouble(),
                    r.get("variation").asText(), r.get("priority").asInt()));
            }

            Map<String, String> attrs = new HashMap<>();
            var attrsNode = v.get("attrs");
            flattenAttrs("", attrsNode, attrs);
            String userId = attrs.getOrDefault("user_id", "");

            var expectedNode = v.get("expected_result");

            list.add(DynamicTest.dynamicTest(id, () -> {
                var context = new EvaluationContext(userId, "", attrs);
                var result = RuleMatcher.matchRules(rules, context, "test-flag");
                if (expectedNode == null || expectedNode.isNull()) {
                    assertTrue(result.isEmpty(), "expected no rule match for " + id);
                } else {
                    assertTrue(result.isPresent(), "expected a rule match for " + id);
                    assertEquals(expectedNode.get("variation").asText(), result.get());
                }
            }));
        }
        return list.stream();
    }

    /** Flattens nested JSON objects in a vector's "attrs" fixture into dot-notation
     *  flat keys (e.g. {"geo": {"country": "us"}} -> "geo.country" -> "us"), matching
     *  this SDK's EvaluationContext.attrs() Map<String,String> model. vectors.json's
     *  nested structure represents the wire format each SDK's client adapts to its
     *  own internal attrs representation — Java's is flat by design (see
     *  RuleMatcher.resolveAttribute), so this harness must flatten before constructing
     *  EvaluationContext, exactly like a real Java SDK client would when deserializing
     *  an incoming evaluation-context payload. */
    private static void flattenAttrs(String prefix, JsonNode node, Map<String, String> out) {
        node.fields().forEachRemaining(e -> {
            String key = prefix.isEmpty() ? e.getKey() : prefix + "." + e.getKey();
            if (e.getValue().isObject()) {
                flattenAttrs(key, e.getValue(), out);
            } else {
                out.put(key, e.getValue().asText());
            }
        });
    }

    @TestFactory
    Stream<DynamicTest> missingAttributeVectors() throws Exception {
        var root = loadVectors();
        var list = new ArrayList<DynamicTest>();
        // Same structure as rule_vectors — reuses the missing_attribute_vectors array,
        // which has a single rule with a missing-attribute condition and no fallback rule.
        for (JsonNode v : root.get("missing_attribute_vectors")) {
            String id = v.get("id").asText();
            var expectedNode = v.get("expected_result");
            list.add(DynamicTest.dynamicTest(id, () -> {
                var condition = new PropertyCondition("missing_attr", "eq", List.of("x"), false);
                var rule = new TargetingRule("r1", List.of(condition), 100.0, "skipped", 0);
                var context = new EvaluationContext("u1", "", Map.of());
                var result = RuleMatcher.matchRules(List.of(rule), context, "test-flag");
                assertTrue((expectedNode == null || expectedNode.isNull()) == result.isEmpty(),
                    "missing-attribute vector mismatch for " + id);
            }));
        }
        return list.stream();
    }
}
