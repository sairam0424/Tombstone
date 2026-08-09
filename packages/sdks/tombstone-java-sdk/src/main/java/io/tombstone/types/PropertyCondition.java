package io.tombstone.types;

import java.util.List;

public record PropertyCondition(
    String attribute,
    String operator,
    List<String> values,
    boolean negate
) {}
