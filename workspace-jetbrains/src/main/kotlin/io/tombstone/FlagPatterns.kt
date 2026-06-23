package io.tombstone

/**
 * Flag detection patterns — port of the 6 regexes in workspace-vscode-ext/src/codelens.ts.
 *
 * Each pattern captures the flag key in group 1.
 * Covers single quotes, double quotes, and backtick literals.
 *
 * Examples matched:
 *   evaluate("my-flag")       evaluate('my-flag')      evaluate(`my-flag`)
 *   isEnabled("my-flag")      isEnabled('my-flag')     isEnabled(`my-flag`)
 *   is_enabled("my-flag")     is_enabled('my-flag')    is_enabled(`my-flag`)
 *   getFlag("my-flag")        getFlag('my-flag')       getFlag(`my-flag`)
 *   flagEnabled("my-flag")    flagEnabled('my-flag')   flagEnabled(`my-flag`)
 *   checkFlag("my-flag")      checkFlag('my-flag')     checkFlag(`my-flag`)
 *
 * Flag key charset: [a-zA-Z0-9._-]+ (dots allowed for namespaced keys like "payments.v2")
 */
object FlagPatterns {

    /** Each entry: (humanName, regex with capturing group 1 = flag key) */
    val ALL: List<Regex> = listOf(
        Regex("""(?<!\w)evaluate\(\s*['"`]([a-zA-Z0-9._\-]+)['"`]"""),
        Regex("""(?<!\w)isEnabled\(\s*['"`]([a-zA-Z0-9._\-]+)['"`]"""),
        Regex("""(?<!\w)is_enabled\(\s*['"`]([a-zA-Z0-9._\-]+)['"`]"""),
        Regex("""(?<!\w)getFlag\(\s*['"`]([a-zA-Z0-9._\-]+)['"`]"""),
        Regex("""(?<!\w)flagEnabled\(\s*['"`]([a-zA-Z0-9._\-]+)['"`]"""),
        Regex("""(?<!\w)checkFlag\(\s*['"`]([a-zA-Z0-9._\-]+)['"`]""")
    )

    /**
     * Scan [text] for all flag key occurrences.
     * Returns a list of (flagKey, charOffset) pairs in document order.
     */
    fun findAll(text: String): List<Pair<String, Int>> {
        val results = mutableListOf<Pair<String, Int>>()
        for (pattern in ALL) {
            for (match in pattern.findAll(text)) {
                val key = match.groupValues[1]
                val offset = match.range.first
                results += key to offset
            }
        }
        // Sort by offset so callers get results in document order
        results.sortBy { it.second }
        return results
    }
}
