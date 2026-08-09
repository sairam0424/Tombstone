package io.tombstone.evaluation;

import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.*;

public class InconclusiveMatchExceptionTest {
    @Test void testIsRuntimeException() {
        var ex = new InconclusiveMatchException("attribute missing");
        assertTrue(ex instanceof RuntimeException);
        assertEquals("attribute missing", ex.getMessage());
    }
}
